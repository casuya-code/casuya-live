"""Unit checks for FeatureStudy replay, monotonicity, and window normalisation."""
import sys
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

import feature_study as _fs
from feature_study import FeatureStudy, MomentumFrame

T0 = 1786600000.0


def _frame(match_id: str, clock: str, ts: float, shots: int, sot: int, danger: int, home: float):
    return {
        "match_id": match_id,
        "sport": "football",
        "league": "england_premier_league",
        "home_team": "A",
        "away_team": "B",
        "kickoff": 1786600000,
        "clock": clock,
        "score": {"home": 0, "away": 1},
        "shots": shots,
        "shots_on_target": sot,
        "dangerous_attacks": danger,
        "possession_home": home,
        "markets": {
            "2H_1X2": {
                "market_id": "2H_1X2",
                "odds": {"home": 2.1, "draw": 3.4, "away": 5.25},
                "updated_at": ts,
            }
        },
    }


# --- Parity: an asof-replayed sequence must fold windows identically to live ---
frames = [_frame("par-1", f"{31 + i}'", T0 + i * 60, 5 + i, 2 + i, 12 + i * 3, 0.55) for i in range(4)]

replay = FeatureStudy()
replay_features = []
for f in frames:
    for snap in replay.observe_many(f, asof=f["markets"]["2H_1X2"]["updated_at"] / 1000.0):
        replay_features.append(replay.feature_vector(snap))

live = FeatureStudy()
live_features = []
clock_ticks = [f["markets"]["2H_1X2"]["updated_at"] / 1000.0 for f in frames]


def _ticks():
    for tick in clock_ticks:
        yield tick


pc = mock.patch.object(_fs.time, "time", side_effect=_ticks())
with pc:
    for f in frames:
        for snap in live.observe_many(f, asof=None):
            live_features.append(live.feature_vector(snap))

assert len(replay_features) == len(live_features) == len(frames), "one snapshot per frame expected"
for i, (a, b) in enumerate(zip(replay_features, live_features)):
    for key in a:
        assert abs(a[key] - b[key]) < 1e-9, f"feature {key} diverged at frame {i}: {a[key]} vs {b[key]}"
print("replay parity: OK")


# --- Monotonicity: a provider counter reset must not rewind the window ---
mono = FeatureStudy()
seen = []
for shots, sot, danger in [(10, 3, 12), (12, 4, 15), (5, 2, 9)]:
    snap = mono.observe(_frame("mon-1", "40'", T0 + 10, shots, sot, danger, 0.5))
    seen.append(snap.shots_total)
assert seen == [10, 12, 12], f"shots_total rewound: {seen}"
print(f"monotonic counter guard: OK  {seen}")


# --- Window normalisation: expected danger scales with observed span ---
study = FeatureStudy()


def _snap(span: float) -> MomentumFrame:
    return MomentumFrame(
        match_id="wn-1",
        market_id="2H_1X2",
        league="england_premier_league",
        home_team="A",
        away_team="B",
        clock="50'",
        score_home=0,
        score_away=1,
        odds={"home": 2.1, "draw": 3.4, "away": 5.25},
        window_start=T0,
        window_end=T0 + span,
        dangerous_attacks=20,
    )


short_exp = study.expected_danger(_snap(10))   # floored at 30s
full_exp = study.expected_danger(_snap(750))   # full configured window
capped_exp = study.expected_danger(_snap(5000))  # capped at WINDOW_SECONDS

assert short_exp > 0, "floor must not zero the expectation"
assert full_exp == 25.0 * short_exp, f"full window should be 25x the 30s floor: {full_exp} vs {short_exp}"
assert abs(capped_exp - full_exp) < 1e-12, "expectation must cap at the configured window"
print(f"window normalisation: OK  f={full_exp:.4f} s={short_exp:.4f}")

print("FEATURE STUDY SMOKE PASSED")