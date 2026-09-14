"""The Study Room: observational momentum engine.

Maintains an isolated 10-15 minute sliding window per live match and tracks
statistical momentum shift metrics (dangerous attacks, possession, shot
accuracy) against baseline expectations loaded from the calibration repo.
"""
from __future__ import annotations

import json
import logging
import os
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

LOG = logging.getLogger("casuya.study")

WINDOW_SECONDS = int(os.getenv("STUDY_WINDOW_SECONDS", "750"))  # 12m30s default
BASE_DIR = Path(__file__).resolve().parents[1]


@dataclass
class MomentumFrame:
    """Aggregated momentum statistics for one windowed match."""

    match_id: str
    market_id: str
    league: str
    home_team: str
    away_team: str
    clock: str
    score_home: int
    score_away: int
    odds: dict[str, float]
    window_start: float
    window_end: float
    shots_total: int = 0
    shots_on_target: int = 0
    dangerous_attacks: int = 0
    possession_home: float = 0.5
    features: dict[str, float] = field(default_factory=dict)


@dataclass
class StudyEntry:
    """One observed event retained inside a match's sliding window."""

    ts: float
    clock: str
    score_home: int
    score_away: int
    shots: int = 0
    shots_on_target: int = 0
    dangerous_attacks: int = 0
    possession_home: float = 0.5


@dataclass
class SettledEntry:
    """Final-state log used to audit predictive drift after settlement."""

    match_id: str
    market_id: str
    final_home: int
    final_away: int
    closing_odds: dict[str, float]
    model_window: list[dict[str, Any]] = field(default_factory=list)
    predicted_side: str = ""  # side the filter actually bet, when known
    closure_delta: float = 0.0


# Clock values that mark period boundaries rather than ingestible momentum.
BOUNDARY_CLOCKS = frozenset({"HALFTIME", "FULLTIME", "HT", "FT"})


def _parse_clock_minute(clock: str) -> float | None:
    """Extract the minute from '31', \"31'\", '45+2'', or 'mm:ss' clocks."""
    text = clock.strip().replace("’", "'").replace("ʼ", "'")
    if not text:
        return None
    # Stoppage "45+2'" -> base minute 45.
    text = text.split("+")[0]
    # "mm:ss" -> mm.
    if ":" in text:
        text = text.split(":")[0]
    text = text.strip().rstrip("'").strip()
    try:
        return float(text)
    except ValueError:
        return None


class FeatureStudy:
    """Sliding-window observer keyed per match and scoped market.

    A frame can be observed with an explicit `asof` timestamp so offline
    calibration replays the exact live windowing/bucketing semantics instead
    of collapsing history onto one wall-clock instant.
    """

    def __init__(self) -> None:
        self._windows: dict[str, list[StudyEntry]] = {}
        self._monotonic: dict[str, dict[str, int]] = {}
        self._baselines = self._load_baselines()

    @staticmethod
    def _load_baselines() -> dict[str, Any]:
        path = BASE_DIR / "models" / "calibration" / "distributions.json"
        try:
            with open(path, encoding="utf-8") as fh:
                return json.load(fh)
        except FileNotFoundError:
            LOG.warning("baseline distributions missing; using empty defaults")
            return {}
        except json.JSONDecodeError:
            LOG.warning("baseline distributions malformed; using empty defaults")
            return {}

    @staticmethod
    def _scores(frame: dict[str, Any]) -> tuple[int, int]:
        """Read the scoreline from flat (sample) or nested (Go) frame shapes."""
        nested = frame.get("score")
        if isinstance(nested, dict):
            return int(nested.get("home", 0)), int(nested.get("away", 0))
        return int(frame.get("score_home", 0)), int(frame.get("score_away", 0))

    @staticmethod
    def _is_boundary(clock: Any) -> bool:
        return str(clock or "").strip().upper() in BOUNDARY_CLOCKS

    def observe(self, frame: dict[str, Any], asof: float | None = None) -> MomentumFrame | None:
        """Append the frame and fold it into a snapshot for its first market.

        Prefer observe_many() when a frame carries several scoped markets.
        """
        snapshots = self.observe_many(frame, asof=asof)
        return snapshots[0] if snapshots else None

    def observe_many(self, frame: dict[str, Any], asof: float | None = None) -> list[MomentumFrame]:
        """Append the frame once, then fold one snapshot per scoped market.

        `asof` pins the observation clock (epoch seconds) so replay matches
        live windowing; when omitted the caller's wall clock is used.
        """
        match_id = frame.get("match_id")
        clock = frame.get("clock", "")
        if not match_id or clock is None or str(clock) == "":
            return []
        if self._is_boundary(clock):
            return []  # boundary markers are not ingestible momentum frames

        mark = frame.get("markets", {})
        if not mark:
            return []
        score_home, score_away = self._scores(frame)
        now = asof if asof is not None else time.time()
        window = self._windows.setdefault(match_id, [])

        # Monotonicity guard: shot/danger counters arrive as live cumulative
        # totals. A provider reset (value lower than previously seen for this
        # match) must not silently rewind the momentum window.
        mono = self._monotonic.setdefault(
            match_id, {"shots": 0, "shots_on_target": 0, "dangerous_attacks": 0}
        )
        shots = int(frame.get("shots", 0))
        shots_on_target = int(frame.get("shots_on_target", 0))
        dangerous_attacks = int(frame.get("dangerous_attacks", 0))
        if shots < mono["shots"]:
            shots = mono["shots"]
        else:
            mono["shots"] = shots
        if shots_on_target < mono["shots_on_target"]:
            shots_on_target = mono["shots_on_target"]
        else:
            mono["shots_on_target"] = shots_on_target
        if dangerous_attacks < mono["dangerous_attacks"]:
            dangerous_attacks = mono["dangerous_attacks"]
        else:
            mono["dangerous_attacks"] = dangerous_attacks

        window.append(
            StudyEntry(
                ts=now,
                clock=str(clock),
                score_home=score_home,
                score_away=score_away,
                shots=shots,
                shots_on_target=shots_on_target,
                dangerous_attacks=dangerous_attacks,
                possession_home=float(frame.get("possession_home", 0.5)),
            )
        )
        # Prune entries older than the study window to bound memory.
        cutoff = now - WINDOW_SECONDS
        window[:] = [e for e in window if e.ts >= cutoff]

        momentum = self._aggregate(window)
        # Counters (shots, danger) arrive as cumulative match totals, so the
        # window total is the latest bucket's max — NOT the sum of bucket
        # maxima (that would multiply the same events once per minute).
        # Possession is a rate, so it averages across buckets.
        shots_total = max((e.shots for e in momentum), default=0)
        shots_on_target = max((e.shots_on_target for e in momentum), default=0)
        dangerous_attacks = max((e.dangerous_attacks for e in momentum), default=0)
        possession_home = sum(e.possession_home for e in momentum) / max(
            len(momentum), 1
        )
        snapshots: list[MomentumFrame] = []
        for market_id, market in mark.items():
            odds: dict[str, float] = {}
            raw_odds = market.get("odds", {}) if isinstance(market, dict) else {}
            for side in ("home", "draw", "away"):
                raw = raw_odds.get(side)
                if raw is not None:
                    odds[side] = float(raw)
            snapshots.append(
                MomentumFrame(
                    match_id=match_id,
                    market_id=market_id,
                    league=str(frame.get("league", "")),
                    home_team=str(frame.get("home_team", "")),
                    away_team=str(frame.get("away_team", "")),
                    clock=str(clock),
                    score_home=window[-1].score_home,
                    score_away=window[-1].score_away,
                    odds=odds,
                    window_start=window[0].ts if window else now,
                    window_end=now,
                    shots_total=shots_total,
                    shots_on_target=shots_on_target,
                    dangerous_attacks=dangerous_attacks,
                    possession_home=possession_home,
                )
            )
        return snapshots

    def collect_settled(self, frame: dict[str, Any]) -> list[SettledEntry]:
        """Emit settled entries when a frame declares a final score."""
        if not self._is_boundary(frame.get("clock")) or str(
            frame.get("clock") or ""
        ).strip().upper() not in ("FULLTIME", "FT"):
            return []
        match_id = frame.get("match_id")
        final_home, final_away = self._scores(frame)
        if match_id:
            self._windows.pop(match_id, None)
        mark = frame.get("markets", {})
        if not mark:
            return []
        out: list[SettledEntry] = []
        for market_id, market in mark.items():
            raw_odds = market.get("odds", {}) if isinstance(market, dict) else {}
            odds = {
                side: float(v)
                for side, v in raw_odds.items()
                if v is not None
            }
            out.append(
                SettledEntry(
                    match_id=match_id,
                    market_id=market_id,
                    final_home=final_home,
                    final_away=final_away,
                    closing_odds=odds,
                )
            )
        return out

    @staticmethod
    def _aggregate(entries: list[StudyEntry]) -> list[StudyEntry]:
        """Fold the window into coarse buckets to dampen sensor noise."""
        if not entries:
            return []
        buckets: dict[int, StudyEntry] = {}
        for e in entries:
            bucket = int(e.ts / 60)  # 60-second buckets
            if bucket not in buckets:
                buckets[bucket] = StudyEntry(
                    ts=e.ts,
                    clock=e.clock,
                    score_home=e.score_home,
                    score_away=e.score_away,
                )
            base = buckets[bucket]
            base.shots = max(base.shots, e.shots)
            base.shots_on_target = max(base.shots_on_target, e.shots_on_target)
            base.dangerous_attacks = max(base.dangerous_attacks, e.dangerous_attacks)
            base.possession_home = e.possession_home
        return sorted(buckets.values(), key=lambda b: b.ts)

    def _baseline_for(self, snapshot: MomentumFrame) -> dict[str, float]:
        dists = self._baselines.get("leagues", {})
        league = dists.get(snapshot.league) or dists.get("default", {})
        return {
            "danger_per_minute": float(league.get("danger_per_minute", 2.5)),
            "possession_home": float(league.get("possession_home", 0.5)),
            "shot_accuracy": float(league.get("shot_accuracy", 0.33)),
        }

    def shot_accuracy(self, snapshot: MomentumFrame) -> float:
        if snapshot.shots_total <= 0:
            return 0.0
        return snapshot.shots_on_target / snapshot.shots_total

    def expected_danger(self, snapshot: MomentumFrame) -> float:
        """Baseline expectation for dangerous attacks over the study window.

        Normalised by the window actually observed so far (not the full
        configured window): a match that just kicked off gets a smaller honest
        expectation than a settled long window, keeping early momentum fair.
        """
        span = snapshot.window_end - snapshot.window_start
        minutes = max(span, 30.0) / 60.0  # floor: one plausible minute bucket
        minutes = min(minutes, WINDOW_SECONDS / 60.0)
        return self._baseline_for(snapshot)["danger_per_minute"] * minutes

    def divergence(self, snapshot: MomentumFrame) -> float:
        """Difference between observed danger surge and baseline expectation."""
        observed = snapshot.dangerous_attacks * (1.0 + self.shot_accuracy(snapshot))
        return observed - self.expected_danger(snapshot)

    def feature_vector(self, snapshot: MomentumFrame) -> dict[str, float]:
        """Standardised feature set consumed by the filter engine."""
        baseline = self._baseline_for(snapshot)
        minute = _parse_clock_minute(snapshot.clock)
        time_pressure = (minute / 90.0) if minute is not None else 1.0
        return {
            "divergence": self.divergence(snapshot),
            "possession_gap": snapshot.possession_home - baseline["possession_home"],
            "shot_accuracy_gap": self.shot_accuracy(snapshot)
            - baseline["shot_accuracy"],
"danger_intensity": snapshot.dangerous_attacks
        / max(self.expected_danger(snapshot), 1e-6),
        "time_pressure": time_pressure,
        "score_pressure": float(snapshot.score_home + snapshot.score_away),
        "home_behind": float(snapshot.score_home < snapshot.score_away),
    }