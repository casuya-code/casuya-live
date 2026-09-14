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
from pathlib import Path
from typing import Any

import requests

LOG = logging.getLogger("casuya.filter")

MIN_ODDS = float(os.getenv("MIN_TRIGGER_ODDS", "5.0"))
MARGIN_SLACK = float(os.getenv("TRUE_PROB_MARGIN_SLACK", "0.020"))  # 2pp safety

# Post-hoc model calibration (remedies runaway logits from unnormalised
# features): temperature scaling divides the raw logistic score before the
# sigmoid (T > 1 compresses probabilities toward 0.5), and MAX_TRUE_PROB caps
# the emitted posterior so live legs can never report 99.9-100% certainty.
MODEL_TEMPERATURE = float(os.getenv("MODEL_TEMPERATURE", "1.5"))
MAX_TRUE_PROB = float(os.getenv("MAX_TRUE_PROB", "0.85"))

# Column order shared by the live score and offline calibration weights.
FEATURE_COLS = [
    "divergence",
    "danger_intensity",
    "possession_gap",
    "shot_accuracy_gap",
    "behind_target",
]

# Offline shim: hand-picked priors used until calibrate.py fits real weights.
SHIM_WEIGHTS = [0.35, 0.30, 0.20, 0.15, 1.20]

_CALIB_PATH = Path(__file__).resolve().parents[1] / "models" / "calibration" / "weights.json"


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
    def _calibrated() -> dict[str, Any] | None:
        """Load the fitted weights once; None keeps the shim in force."""
        if not hasattr(FilterEngine, "_calib"):
            calib = None
            try:
                with open(_CALIB_PATH, encoding="utf-8") as fh:
                    loaded = json.load(fh)
                if isinstance(loaded.get("weights"), list) and len(loaded["weights"]) == len(FEATURE_COLS):
                    calib = loaded
            except (OSError, json.JSONDecodeError):
                calib = None
            FilterEngine._calib = calib
        return FilterEngine._calib

    @staticmethod
    def score_inputs(snapshot: Any, side: str) -> list[float]:
        """Normalised x-vector (FEATURE_COLS order) for the logistic score.

        Features are home-centric, so the away perspective inverts the gaps.
        This is the exact input surface both the live score and calibrate.py
        fit against, so fitted weights hold at runtime.
        """
        features = getattr(snapshot, "features", {}) or {}
        invert = side == "away"
        behind = float(features.get("home_behind", 0.0))
        if not invert:
            behind_target = behind
        elif features.get("score_pressure", 0.0) > 0 and not behind:
            behind_target = 1.0  # away is the side behind (home leads)
        else:
            behind_target = 0.0
        gap = features.get("possession_gap", 0.0)
        poss_gap = -gap if invert else gap
        acc = features.get("shot_accuracy_gap", 0.0)
        shot_gap = -acc if invert else acc
        div = features.get("divergence", 0.0)
        divergence = -div if invert else div
        return [
            divergence,
            features.get("danger_intensity", 1.0),
            poss_gap,
            shot_gap,
            behind_target,
        ]

    @classmethod
    def true_probability(cls, snapshot: Any, side: str) -> float:
        """Calibrated posterior derived from momentum divergence.

        If calibrate.py has written weights.json, the weight vector + bias
        replace the offline shim (SHIM_WEIGHTS, no bias). Draw probability is
        derived from the two opposing legs to keep the market closed.
        """
        features = getattr(snapshot, "features", {}) or {}
        if side == "draw":
            away_p = cls.true_probability(snapshot, "away")
            home_p = cls.true_probability(snapshot, "home")
            return max(0.0, 1.0 - away_p - home_p)

        x = cls.score_inputs(snapshot, side)
        calib = cls._calibrated()
        if calib is not None:
            weights = [float(w) for w in calib["weights"]]
            bias = float(calib.get("bias", 0.0))
        else:
            weights = SHIM_WEIGHTS
            bias = 0.0
        score = (bias + sum(w * xi for w, xi in zip(weights, x))) / MODEL_TEMPERATURE
        prob = 1.0 / (1.0 + (2.718281828459045 ** (-score)))  # bounded (0,1)
        return min(prob, MAX_TRUE_PROB)

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