"""AI self-correction and diagnostic engine.

Executes automatically when a tracked bet settles as LOST. Compares mid-match
predictive models against final match logs to detect calculation drift and
emits structural JSON diagnostics indicating which line items, weights, or
variables inside feature_study.py require tuning.
"""
from __future__ import annotations

import json
import logging
import os
import time
from dataclasses import dataclass, field
from typing import Any

LOG = logging.getLogger("casuya.diagnostics")

DIAG_CHANNEL = os.getenv("DIAGNOSTICS_CHANNEL", "diagnostics:events")


@dataclass
class DiagnosticRecord:
    """One human-readable audit entry per settled LOST bet."""

    match_id: str
    market_id: str
    final_home: int
    final_away: int
    predicted_side: str
    drift_score: float
    drift_map: dict[str, float] = field(default_factory=dict)
    recommendations: list[str] = field(default_factory=list)
    emitted_at: float = field(default_factory=lambda: time.time())


class DiagnosticsEngine:
    """Compares model output against ground truth to expose drift."""

    def __init__(self, diff_baseline: float | None = None) -> None:
        # A prediction is LOST when the model's favourite side did not win.
        self.diff_baseline = diff_baseline if diff_baseline is not None else 0.30

    def audit(self, settled) -> DiagnosticRecord | None:
        """Evaluate one settled entry and return a structural diagnostic."""
        record = self._build_record(settled)
        if record is None:
            return None
        self._emit(record)
        return record

    def _build_record(self, settled) -> DiagnosticRecord | None:
        try:
            final_home = int(settled.final_home)
            final_away = int(settled.final_away)
        except (TypeError, ValueError):
            return None
        odds = getattr(settled, "closing_odds", {}) or {}
        if not odds:
            return None
        # Prefer the side the filter actually bet (carried on the entry by
        # the broker). Fall back to the longest-odds leg, which is what a
        # high-odds value system most likely took.
        side = getattr(settled, "predicted_side", "") or max(odds, key=odds.get)
        predicted_win = self._resolved(side, final_home, final_away)
        if predicted_win:
            return None  # WON: no correction required yet
        drift_map = {
            "variance_score": self._variance(odds, side),
            "overconfidence": round(max(odds.values()) / (max(odds.values()) + 1.0), 4),
            "closure_delta": float(getattr(settled, "closure_delta", 0.0)),
        }
        drift_score = sum(drift_map.values()) / max(len(drift_map), 1)
        recommendations = self._recommendations(drift_map)
        return DiagnosticRecord(
            match_id=settled.match_id,
            market_id=getattr(settled, "market_id", ""),
            final_home=final_home,
            final_away=final_away,
            predicted_side=side,
            drift_score=round(drift_score, 4),
            drift_map=drift_map,
            recommendations=recommendations,
        )

    @staticmethod
    def _resolved(side: str, home: int, away: int) -> bool:
        if side == "home":
            return home > away
        if side == "away":
            return away > home
        return home == away

    @staticmethod
    def _variance(odds: dict[str, float], predicted: str) -> float:
        """Brier-style mismatch: squared error of the predicted pool share.

        We already know the bet LOST, so the resolved indicator is False for
        the predicted side. A high variance score flags an over-priced
        favourite whose posterior was miscalibrated.
        """
        pool_share = 1.0 / float(odds.get(predicted, 2.0))
        return round((pool_share - 0.0) ** 2, 4)

    def _recommendations(self, drift_map: dict[str, float]) -> list[str]:
        out: list[str] = []
        if drift_map.get("variance_score", 0.0) > self.diff_baseline:
            out.append(
                "feature_study.divergence: widen the danger-surge weighting in "
                "the momentum aggregation window"
            )
        if drift_map.get("overconfidence", 0.0) > self.diff_baseline:
            out.append(
                "filter_engine.true_probability: apply shrinkage to the logistic "
                "score before comparing against implied probability"
            )
        return out

    def _emit(self, record: DiagnosticRecord) -> None:
        # Local sink. The broker (main.py) publishes the returned record to
        # DIAG_CHANNEL so the web relay can stream it to the dashboard.
        LOG.warning(
            "diagnostic %s drift=%.4f note=%s",
            record.match_id,
            record.drift_score,
            json.dumps(record.drift_map, sort_keys=True),
        )