"""High-odds filter and execution trigger.

Applies the core mathematical selection criteria:

    Trigger Condition = (Market Odds >= 5.0) AND (Calculated True Probability
                        > Implied Bookmaker Probability)

The side selected is the one with the strongest positive edge, not merely the
highest quote. Passing verdicts generate a cryptographically signed execution
payload dispatched to the execution engine over private network channels.
"""
from __future__ import annotations

import base64
import hashlib
import hmac as hmac_mod
import json
import logging
import os
import time
from dataclasses import dataclass, field
from typing import Any

import requests

LOG = logging.getLogger("casuya.filter")

MIN_ODDS = float(os.getenv("MIN_TRIGGER_ODDS", "5.0"))
MARGIN_SLACK = float(os.getenv("TRUE_PROB_MARGIN_SLACK", "0.020"))  # 2pp safety


@dataclass
class Verdict:
    """Result of evaluating one market snapshot against the trigger rule."""

    should_execute: bool
    match_id: str
    market_id: str
    side: str
    odds: float
    implied_prob: float
    true_prob: float
    features: dict[str, float] = field(default_factory=dict)
    reasons: list[str] = field(default_factory=list)

    def to_signed_payload(self, key: str) -> str:
        """Generated signed envelope with the canonical payload embedded."""
        payload = {
            "match_id": self.match_id,
            "market_id": self.market_id,
            "side": self.side,
            "odds": self.odds,
            "true_prob": self.true_prob,
            "implied_prob": self.implied_prob,
            "amount": _stake(),
            "ts": int(time.time() * 1000),
        }
        # Both halves are base64url WITHOUT padding: execution-engine-go
        # decodes with base64.RawURLEncoding, which rejects trailing '='.
        encoded = (
            base64.urlsafe_b64encode(
                json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
            )
            .decode()
            .rstrip("=")
        )
        return f"{encoded}.{sign_payload(payload, key)}"


class FilterEngine:
    """Evaluates momentum snapshots and dispatches signed orders."""

    def __init__(self, base_url: str, signer_key: str | None = None) -> None:
        self.base_url = base_url.rstrip("/")
        self.signer_key = signer_key or os.getenv("INTERNAL_AUTH_SECRET", "")
        if not self.signer_key:
            raise RuntimeError("INTERNAL_AUTH_SECRET is required for payload signing")

    @staticmethod
    def implied_probability(odds: float) -> float:
        """Probability implied by the bookmaker quote."""
        return 1.0 / odds

    def best_side(self, odds: dict[str, float], snapshot: Any) -> tuple[str, float, float]:
        """Return (side, true_prob, implied_prob) of the strongest edge.

        The edge is measured per-leg; sides below the odds floor are
        excluded. A negative best edge is still returned — evaluate()
        applies the margin-slack gate and rejects it there.
        """
        best: tuple[str, float, float] | None = None
        for side in ("home", "away", "draw"):
            price = float(odds.get(side, 0.0))
            if price < MIN_ODDS:
                continue
            true_prob = FilterEngine.true_probability(snapshot, side)
            implied = FilterEngine.implied_probability(price)
            edge = true_prob - implied
            if best is None or edge > best[1] - best[2]:
                best = (side, true_prob, implied)
        if best is None:
            return "", 0.0, 0.0
        return best

    def evaluate(self, snapshot: Any) -> Verdict:
        """Apply the trigger rule to a momentum snapshot."""
        odds = snapshot.odds or {}
        if not odds:
            return Verdict(False, snapshot.match_id, snapshot.market_id, "", 0.0, 0.0, 0.0)
        side, true_prob, implied = self.best_side(odds, snapshot)
        price = float(odds.get(side, 0.0))
        if not side:
            return Verdict(
                False, snapshot.match_id, snapshot.market_id, side, price,
                implied, true_prob,
                features=getattr(snapshot, "features", {}),
                reasons=[f"no market leg >= {MIN_ODDS:.2f}"],
            )
        should = true_prob > implied + MARGIN_SLACK
        return Verdict(
            should_execute=should,
            match_id=snapshot.match_id,
            market_id=snapshot.market_id,
            side=side,
            odds=price,
            implied_prob=round(implied, 4),
            true_prob=round(true_prob, 4),
            features=getattr(snapshot, "features", {}),
            reasons=[] if should else [
                f"true {true_prob:.3f} <= implied {implied:.3f} + slack"
            ],
        )

    @staticmethod
    def true_probability(snapshot: Any, side: str) -> float:
        """Calibrated posterior derived from momentum divergence.

        A weighted logistic score over the Study Room features. Weights live in
        the calibration matrix; this calibrated shim is replaced post-training.
        Features are home-centric, so the away perspective inverts the gaps.
        """
        features = getattr(snapshot, "features", {}) or {}
        if side == "draw":
            away_p = FilterEngine.true_probability(snapshot, "away")
            home_p = FilterEngine.true_probability(snapshot, "home")
            return max(0.0, 1.0 - away_p - home_p)

        invert = side == "away"
        behind_target = features.get("home_behind", 0.0) if not invert else 0.0
        if invert and features.get("score_pressure", 0.0) > 0 and not features.get("home_behind", 0.0):
            behind_target = 1.0  # away is the side behind (home leads)
        gap = features.get("possession_gap", 0.0)
        pos_gap = -gap if invert else gap
        acc_gap = features.get("shot_accuracy_gap", 0.0)
        shot_gap = -acc_gap if invert else acc_gap
        divergence = features.get("divergence", 0.0)
        div = -divergence if invert else divergence
        score = (
            div * 0.35
            + features.get("danger_intensity", 1.0) * 0.30
            + pos_gap * 0.20
            + shot_gap * 0.15
            + behind_target * 1.20
        )
        return 1.0 / (1.0 + (2.718281828459045 ** (-score)))  # bounded (0,1)

    def dispatch(self, verdict: Verdict) -> dict[str, Any]:
        """Transmit a signed execution payload over the private mesh."""
        if not verdict.should_execute:
            return {"status": "skipped"}
        envelope = verdict.to_signed_payload(self.signer_key)
        try:
            resp = requests.post(
                f"{self.base_url}/execute",
                json={"envelope": envelope},
                headers={"X-Internal-Token": self.signer_key},
                timeout=2.0,
            )
            resp.raise_for_status()
            return resp.json()
        except requests.RequestException as exc:
            LOG.error("execution dispatch failed: %s", exc)
            return {"status": "error", "detail": str(exc)}


def _stake() -> float:
    # Parameterised per-market; constant shim until risk-engine calibration.
    return float(os.getenv("STAKE_AMOUNT", "25.0"))


def sign_payload(payload: dict[str, Any], key: str) -> str:
    """HMAC-SHA256 signature over the canonical JSON payload.

    Stdlib-only scheme so execution-engine-go verifies the exact same digest
    format: base64url(HMAC_SHA256(key, body)) without padding.
    """
    body = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode()
    digest = hmac_mod.new(key.encode(), body, hashlib.sha256).digest()
    return base64.urlsafe_b64encode(digest).decode().rstrip("=")