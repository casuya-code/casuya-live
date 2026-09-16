"""Self-contained checks for the calibration path (no Redis required)."""
import doctest
import math
import os
import random
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))

from filter_engine import FEATURE_COLS, FilterEngine, SHIM_WEIGHTS, MODEL_TEMPERATURE, MAX_TRUE_PROB

# --- score_inputs mirrors the shim's original home/away inversion math ---
from types import SimpleNamespace

SNAP = SimpleNamespace(
    features={
        "divergence": -4.0,
        "danger_intensity": 0.77,
        "possession_gap": 0.07,
        "shot_accuracy_gap": 0.10,
        "score_pressure": 1.0,
        "home_behind": 1.0,
    }
)

home_x = FilterEngine.score_inputs(SNAP, "home")
away_x = FilterEngine.score_inputs(SNAP, "away")
assert home_x[0] == -4.0, "home retains negative divergence gap"
assert away_x[0] == 4.0, "away inverts divergence"
assert away_x[2] == -0.07, "away inverts possession gap"
assert home_x[4] == 1.0, "home keeps home_behind"
assert away_x[4] == 0.0, "away clears home_behind with home already behind"
assert len(home_x) == len(FEATURE_COLS) == len(SHIM_WEIGHTS)
print(f"score_inputs: OK  samples={home_x}")


# Shim fallback must hold regardless of any weights.json on disk, so the test
# points the loader at a missing file and clears its cached lookup.
from pathlib import Path as _Path

import filter_engine as _fe

_fake_missing = _Path("definitely-not-a-calibration.json")
_orig_calib_path = _fe._CALIB_PATH
_flexed = False


def _force_shim():
    global _flexed
    if not _flexed:
        _fe._CALIB_PATH = _fake_missing
        if hasattr(FilterEngine, "_calib"):
            del FilterEngine._calib
        _flexed = True


_force_shim()


def shim(side):
    x = FilterEngine.score_inputs(SNAP, side)
    z = sum(w * xi for w, xi in zip(SHIM_WEIGHTS, x))
    # Mirror true_probability's transforms: temperature scaling + cap.
    p = 1.0 / (1.0 + math.exp(-z / MODEL_TEMPERATURE))
    return min(p, MAX_TRUE_PROB)


assert FilterEngine.true_probability(SNAP, "home") == shim("home")
assert FilterEngine.true_probability(SNAP, "away") == shim("away")
draw_p = FilterEngine.true_probability(SNAP, "draw")
assert draw_p == max(0.0, 1 - shim("home") - shim("away")), "draw derives from the closed market and clamps"
print("true_probability shim fallback: OK")


# --- fit_logistic recovers a synthetic logistic relationship ---
from calibrate import fit_logistic, sigmoid

random.seed(11)
X, y = [], []
true_b = 1.0
true_w = [1.0, 2.0, -1.5]
for _ in range(4000):
    row = [random.uniform(-2, 2) for _ in range(3)]
    p = sigmoid(true_b + sum(w * xi for w, xi in zip(true_w, row)))
    y.append(1 if random.random() < p else 0)
    X.append(row)
fitted, loss = fit_logistic(X, y, iters=6000)
assert abs(fitted[0] - true_b) < 0.35, f"bias: fitted {fitted[0]} vs {true_b}"
for name, fitted_w, expected in zip(["w1", "w2", "w3"], list(fitted[1:]), true_w):
    assert abs(fitted_w - expected) < 0.35, f"{name}: fitted {fitted_w} vs {expected}"
print(f"fit_logistic: OK  {list(round(v, 3) for v in fitted)}  loss={loss:.3f}")


# --- final-score / band parsing honour the nested stream shape ---
from calibrate import _band_label, _final_score

assert _final_score({"score": {"home": 1, "away": 2}}) == (1, 2), "nested score shape"
assert _final_score({"score_home": 3, "score_away": 0}) == (3, 0), "flat score shape"
assert _band_label(90) == "75-90", "stoppage minute clamps into the last band"
assert _band_label(31) == "30-45"
assert _band_label(0) == "0-15"
print("final-score / band parsing: OK")


# --- decay matrix monotone (freshest bucket weighs most) ---
from calibrate import DECAY_PATH, write_decay_matrix
from csv import reader as csv_reader

write_decay_matrix()
with open(DECAY_PATH, encoding="utf-8") as fh:
    rows = list(csv_reader(fh))
weights = [float(r[2]) for r in rows[1:]]
assert len(weights) >= 12, "one weight per 60s bucket across the window"
assert weights == sorted(weights), "decay rises toward the freshest bucket"
assert 0 < weights[0] < 1, "oldest bucket still carries weight"
print(f"decay_matrix: OK  {len(weights)} buckets")


# --- L2 ridge must shrink weights without destroying the fit ---
big_l2_fit, _ = fit_logistic(X, y, iters=6000, l2=1.0)
small_l2_fit, _ = fit_logistic(X, y, iters=6000, l2=1e-6)
assert max(abs(w) for w in big_l2_fit[1:]) < max(abs(w) for w in small_l2_fit[1:]) < 3.0
assert abs(fitted[0] - big_l2_fit[0]) < 1.0, "L2 must not cast the bias adrift"
print(f"l2 regularization: OK  max|w| l2=1.0:{max(abs(w) for w in big_l2_fit[1:]):.3f} l2=1e-6:{max(abs(w) for w in small_l2_fit[1:]):.3f}")


# --- logloss / baseline_logloss / auc behave like proper metrics ---
from calibrate import auc, baseline_logloss, logloss

assert logloss([1, 1, 0], [0.99, 0.99, 0.01]) < logloss([1, 1, 0], [0.5, 0.5, 0.5])
assert abs(baseline_logloss([1, 1, 0]) - logloss([1, 1, 0], [2 / 3] * 3)) < 1e-9
assert auc([1, 1, 0, 0], [0.9, 0.8, 0.2, 0.1]) == 1.0, "perfect ranking"
assert auc([1, 1, 0, 0], [0.1, 0.2, 0.9, 0.8]) == 0.0, "perfectly reversed"
assert auc([1, 0, 1, 0], [0.5, 0.5, 0.5, 0.5]) == 0.5, "all ties"
print("metrics: OK")


# --- adoption gate: enforce signal, history, and beating-baseline ---
from calibrate import _adoptable

feat = [1e-2] * 5
assert _adoptable(rows=200, val_rows=40, weights=feat, val_ll=0.6, base_ll=0.7) is True
assert _adoptable(rows=50, val_rows=40, weights=feat, val_ll=0.4, base_ll=0.7) is False, "too few rows"
assert _adoptable(rows=200, val_rows=10, weights=feat, val_ll=0.4, base_ll=0.7) is False, "too few val rows"
assert _adoptable(rows=200, val_rows=40, weights=[0.0] * 5, val_ll=0.4, base_ll=0.7) is False, "no signal"
assert _adoptable(rows=200, val_rows=40, weights=feat, val_ll=0.8, base_ll=0.7) is False, "worse than baseline"
print("adoption gate: OK")


# --- collect_rows: nested scores, 5-tuple shape, chronological cycle sizes ---
from calibrate import collect_rows

CYCLE = [
    {
        "match_id": "cb-1",
        "league": "england_premier_league",
        "kickoff": 1000,
        "clock": "40'",
        "score": {"home": 0, "away": 1},
        "shots": 8,
        "shots_on_target": 3,
        "dangerous_attacks": 15,
        "possession_home": 0.6,
        "received_at": "2026-08-01T12:00:00+00:00",
        "markets": {"2H_1X2": {"market_id": "2H_1X2", "odds": {"home": 2.1, "draw": 3.4, "away": 5.25}}},
    },
    {
        "match_id": "cb-1",
        "league": "england_premier_league",
        "kickoff": 1000,
        "clock": "55'",
        "score": {"home": 0, "away": 2},
        "shots": 11,
        "shots_on_target": 4,
        "dangerous_attacks": 20,
        "possession_home": 0.55,
        "received_at": "2026-08-01T12:15:00+00:00",
        "markets": {"2H_1X2": {"market_id": "2H_1X2", "odds": {"home": 2.1, "draw": 3.4, "away": 5.25}}},
    },
    {
        "match_id": "cb-1",
        "league": "england_premier_league",
        "kickoff": 1000,
        "clock": "FULLTIME",
        "score": {"home": 0, "away": 2},
        "shots": 12,
        "shots_on_target": 5,
        "dangerous_attacks": 24,
        "possession_home": 0.52,
        "markets": {},
    },
]
CX, Cy, Cimpl, Csizes, Creport = collect_rows([CYCLE])
assert len(CX) == len(Cy) == len(Cimpl) == 4, f"2 ingestible frames x 2 sides => 4 rows, got {len(CX)}"
assert Csizes == [4], "one cycle => one size entry"
assert Creport["cycles"] == 1 and Creport["frames"] == 3
assert Cy == [0, 1, 0, 1], "away won 0-2 => home legs lose, away legs win"
assert all(0.0 < p < 1.0 for p in Cimpl)
print(f"collect_rows: OK  rows={len(CX)} implied={[round(p, 3) for p in Cimpl]}")

print("CALIBRATE SMOKE PASSED")