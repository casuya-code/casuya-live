"""Self-contained checks for the calibration path (no Redis required)."""
import doctest
import math
import os
import random
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))

from filter_engine import FEATURE_COLS, FilterEngine, SHIM_WEIGHTS

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


def shim(side):
    x = FilterEngine.score_inputs(SNAP, side)
    z = sum(w * xi for w, xi in zip(SHIM_WEIGHTS, x))
    return 1.0 / (1.0 + math.exp(-z))


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

print("CALIBRATE SMOKE PASSED")