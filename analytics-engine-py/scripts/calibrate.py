"""Offline calibration for the Study Room win-probability model.

Reads the durable frame history from the Redis Stream `matches:live`
(ingestor writes the full frame under `data`), regroups frames into complete
match cycles, and fits a pure-stdlib logistic regression over the exact
feature vector the live filter consumes (FilterEngine.score_inputs).

Writes models/calibration/weights.json, which FilterEngine.true_probability
loads at runtime (shim weights remain in force while absent). With
--update-distributions it also recomputes the per-league baselines consumed by
FeatureStudy (danger_per_minute, possession_home, shot_accuracy).

Usage:
    python scripts/calibrate.py --frames 2000 [--update-distributions]
"""
from __future__ import annotations

import argparse
import json
import logging
import math
import os
import sys
import time
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path
from statistics import fmean
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

import redis as redis_sync

from feature_study import BASE_DIR, FeatureStudy, _parse_clock_minute
from filter_engine import FEATURE_COLS, FilterEngine

LOG = logging.getLogger("casuya.calibrate")
STREAM = os.getenv("ANALYTICS_IN_CHANNEL", "matches:live")
WEIGHTS_PATH = BASE_DIR / "models" / "calibration" / "weights.json"
DISTS_PATH = BASE_DIR / "models" / "calibration" / "distributions.json"


def sigmoid(z: float) -> float:
    """Numerically bounded logistic."""
    if z >= 0:
        e = math.exp(-z)
        return 1.0 / (1.0 + e)
    e = math.exp(z)
    return e / (1.0 + e)


def fit_logistic(
    X: list[list[float]],
    y: list[int],
    lr: float = 0.1,
    iters: int = 6000,
    tol: float = 1e-9,
) -> tuple[list[float], float]:
    """Batch gradient descent over the Bernoulli likelihood.

    Features are standardised internally (fast, scale-free convergence) and
    the fitted weight vector is mapped back to the original feature scale.
    Index 0 of the result is the intercept (bias). Returns (coefficients,
    final negative log-likelihood).
    """
    n = len(X)
    k = len(X[0])
    mu = [sum(row[j] for row in X) / n for j in range(k)]
    sd = []
    for j in range(k):
        var = sum((row[j] - mu[j]) ** 2 for row in X) / n
        sd.append(math.sqrt(var) if var > 1e-12 else 1.0)
    Xn = [[(v - mu[j]) / sd[j] for j, v in enumerate(row)] for row in X]

    w = [0.0] * (k + 1)
    loss = float("inf")
    for _ in range(iters):
        grads = [0.0] * (k + 1)
        loss_sum = 0.0
        for row, yi in zip(Xn, y):
            z = w[0] + sum(wi * xi for wi, xi in zip(w[1:], row))
            p = sigmoid(z)
            err = p - yi
            grads[0] += err
            for j, xi in enumerate(row):
                grads[j + 1] += err * xi
            loss_sum += -(yi * math.log(max(p, 1e-12)) + (1 - yi) * math.log(max(1 - p, 1e-12)))
        for j in range(k + 1):
            w[j] -= lr * grads[j] / n
        loss_now = loss_sum / n
        if abs(loss_now - loss) <= tol:
            break
        loss = loss_now

    w0 = w[0] - sum(w[j + 1] * mu[j] / sd[j] for j in range(k))
    return [w0, *(w[j + 1] / sd[j] for j in range(k))], loss


def load_frames(redis_url: str, limit: int) -> list[dict[str, Any]]:
    """Fetch the most recent `limit` full frames in chronological order."""
    r = redis_sync.from_url(redis_url, decode_responses=True)
    try:
        rows = r.xrevrange(STREAM, count=limit)
        frames: list[dict[str, Any]] = []
        for _, fields in rows:
            raw = fields.get("data")
            if not raw:
                continue  # legacy entries predate full-frame persistence
            try:
                frames.append(json.loads(raw))
            except json.JSONDecodeError:
                continue
        return list(reversed(frames))
    finally:
        r.close()


def group_cycles(frames: list[dict[str, Any]]) -> list[list[dict[str, Any]]]:
    """Group consecutive frames into (match_id, kickoff) cycles."""
    groups: dict[tuple[str, int], list[dict[str, Any]]] = {}
    order: list[tuple[str, int]] = []
    for payload in frames:
        match_id = payload.get("match_id")
        kickoff_raw = payload.get("kickoff", 0)
        try:
            kickoff = int(kickoff_raw)
        except (TypeError, ValueError):
            try:
                kickoff = int(datetime.fromisoformat(str(kickoff_raw)).timestamp())
            except ValueError:
                kickoff = 0
        key = (str(match_id), kickoff)
        if key not in groups:
            groups[key] = []
            order.append(key)
        groups[key].append(payload)
    return [groups[k] for k in order]


def final_score(cycle: list[dict[str, Any]]) -> tuple[int, int] | None:
    """The declared final score that closes a cycle.

    Prefers an explicit FULLTIME marker; the mock ends at minute 90, so
    otherwise the highest-clock frame makes the final score. Only near-final
    frames (>= 88') count, so mid-run partial cycles are never mislabelled.
    """
    for payload in reversed(cycle):
        if str(payload.get("clock", "")).strip().upper() in ("FULLTIME", "FT"):
            return int(payload.get("score_home", 0)), int(payload.get("score_away", 0))
    best = None
    for payload in cycle:
        minute = _parse_clock_minute(str(payload.get("clock", "")))
        if minute is None or minute < 88:
            continue
        if best is None or minute > best[0]:
            best = (minute, payload)
    if best is None:
        return None
    late = best[1]
    return int(late.get("score_home", 0)), int(late.get("score_away", 0))


def collect_rows(
    cycles: list[list[dict[str, Any]]],
) -> tuple[list[list[float]], list[int], dict[str, int]]:
    """Build (x, y) training pairs mirroring live-model inputs exactly."""
    X: list[list[float]] = []
    y: list[int] = []
    report: dict[str, int] = {"cycles": 0, "frames": 0, "snapshots": 0}
    for cycle in cycles:
        score = final_score(cycle)
        if score is None or len(cycle) < 2:
            continue
        home, away = score
        report["cycles"] += 1
        report["frames"] += len(cycle)
        study = FeatureStudy()
        for payload in cycle:
            for snap in study.observe_many(payload):
                snap.features = study.feature_vector(snap)
                for side in ("home", "away"):
                    X.append(FilterEngine.score_inputs(snap, side))
                    y.append(1 if _side_won(side, home, away) else 0)
                    report["snapshots"] += 1
    return X, y, report


def _side_won(side: str, home: int, away: int) -> bool:
    if side == "home":
        return home > away
    if side == "away":
        return away > home
    return home == away


def _adoptable(rows: int, weights: list[float]) -> bool:
    """Adoption gate: refuse a signal-less fit that would silence trading.

    A calibration whose features carry no weight (all ~0) collapses every
    leg's posterior to a floor probability and blocks all executions — worse
    than the shim. Only adopt when there is real feature signal and enough
    history behind the fit.
    """
    if rows < 100:
        return False
    return max(abs(w) for w in weights) >= 1e-3


def calibrate_report(X, y, weights, bias) -> None:
    """In-sample honesty checks: accuracy, Brier, and per-decile reliability."""
    correct = 0
    brier = 0.0
    buckets: dict[int, list[int]] = defaultdict(list)
    for row, yi in zip(X, y):
        z = bias + sum(w * xi for w, xi in zip(weights, row))
        p = sigmoid(z)
        correct += int((p >= 0.5) == bool(yi))
        brier += (p - yi) ** 2
        buckets[int(p * 10)].append(yi)
    n = len(y)
    print(f"  accuracy {correct / n:.3f}  brier {brier / n:.4f}")
    for decile in sorted(buckets):
        hits = buckets[decile]
        print(f"  pred {decile * 10:>2}-{decile * 10 + 10:<2}%: n={len(hits):>3} actual={fmean(hits):.3f}")


def recompute_distributions(frames: list[dict[str, Any]]) -> dict[str, Any]:
    """Refit league baselines from observed frame statistics."""
    acc: dict[str, dict[str, list[float]]] = defaultdict(lambda: defaultdict(list))
    for payload in frames:
        minute = _parse_clock_minute(str(payload.get("clock", "")))
        if minute is None or minute <= 0:
            continue
        league = str(payload.get("league", "default"))
        danger = int(payload.get("dangerous_attacks", 0))
        shots = int(payload.get("shots", 0))
        sot = int(payload.get("shots_on_target", 0))
        acc[league]["danger_per_minute"].append(danger / minute)
        acc[league]["possession_home"].append(float(payload.get("possession_home", 0.5)))
        acc[league]["shot_accuracy"].append(sot / shots if shots else 0.0)

    dists = {"version": 2, "generated_at": _now_iso()}
    leagues = {}
    for league, values in acc.items():
        if len(values["danger_per_minute"]) < 3:
            continue
        leagues[league] = {key: round(fmean(vals), 3) for key, vals in values.items()}
    dists["leagues"] = leagues or {"default": {}}
    return dists


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def main() -> None:
    parser = argparse.ArgumentParser(description="Fit the Study Room win-probability model.")
    parser.add_argument("--redis-url", default=os.getenv("REDIS_URL", "redis://localhost:6379/0"))
    parser.add_argument("--frames", type=int, default=2000, help="max frames to read from history")
    parser.add_argument("--update-distributions", action="store_true", help="recompute baselines")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(message)s")
    print(f"read: {args.frames} frames from stream {STREAM}")

    frames = load_frames(args.redis_url, args.frames)
    print(f"  -> {len(frames)} full frames")
    if len(frames) < 8:
        print("not enough history; let the mock run a few cycles first")
    cycles = group_cycles(frames)
    X, y, report = collect_rows(cycles)
    print(
        f"rows {len(X)} across {report['cycles']} complete cycles "
        f"({report['frames']} frames, {report['snapshots']} snapshots)"
    )
    if len(X) < 20:
        print("insufficient rows; exiting without writing weights")
        return

    biases_weights, loss = fit_logistic(X, y)
    bias, weights = biases_weights[0], list(biases_weights[1:])
    print(f"fit ok  final_neg_ll {loss:.4f}")
    print(f"bias {bias:+.4f}")
    for name, w in zip(FEATURE_COLS, weights):
        print(f"  {name:<20} {w:+.4f}")
    calibrate_report(X, y, weights, bias)

    if not _adoptable(rows=len(X), weights=weights):
        print(
            "fit has no usable signal (draw-heavy history or too few rows); "
            "keeping the shim in force (no weights.json written)"
        )
        return

    WEIGHTS_PATH.write_text(
        json.dumps(
            {
                "version": 2,
                "generated_at": _now_iso(),
                "rows": len(X),
                "features": FEATURE_COLS,
                "bias": round(bias, 6),
                "weights": [round(w, 6) for w in weights],
            },
            indent=2,
            sort_keys=True,
        ),
        encoding="utf-8",
    )
    print(f"wrote {WEIGHTS_PATH}")

    if args.update_distributions:
        dists = recompute_distributions(frames)
        DISTS_PATH.write_text(json.dumps(dists, indent=2, sort_keys=True), encoding="utf-8")
        print(f"wrote {DISTS_PATH} with leagues: {', '.join(dists['leagues']) if dists['leagues'] else '(none)'}")


if __name__ == "__main__":
    main()