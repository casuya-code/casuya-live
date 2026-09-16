"""Offline calibration for the Study Room win-probability model.

Reads the durable frame history from the Redis Stream `matches:live`
(ingestor writes the full frame under `data`), regroups frames into complete
match cycles, and fits a pure-stdlib logistic regression over the exact
feature vector the live filter consumes (FilterEngine.score_inputs).

Writes models/calibration/weights.json, which FilterEngine.true_probability
loads at runtime (shim weights remain in force while absent). With
--update-distributions it also recomputes the per-league baselines consumed by
FeatureStudy (danger_per_minute, possession_home, shot_accuracy).

Each run also regenerates the reference datasets:
  decay_matrix.csv             -> time-decay weighting across the study window
  season_trends.csv            -> per-league outcome + minute-band baselines
  calibration_timeseries.csv   -> one drift-snapshot row appended per run

Usage:
    python scripts/calibrate.py --frames 2000 [--update-distributions]
"""
from __future__ import annotations

import argparse
import csv
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
CALIB_DIR = BASE_DIR / "models" / "calibration"
WEIGHTS_PATH = CALIB_DIR / "weights.json"
DISTS_PATH = CALIB_DIR / "distributions.json"
DECAY_PATH = CALIB_DIR / "decay_matrix.csv"
TRENDS_PATH = CALIB_DIR / "season_trends.csv"
TIMESERIES_PATH = CALIB_DIR / "calibration_timeseries.csv"


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
    l2: float = 1e-4,
) -> tuple[list[float], float]:
    """Batch gradient descent with L2 ridge penalty (excludes bias).

    Features are standardised internally (fast, scale-free convergence) and
    the fitted weight vector is mapped back to the original feature scale.
    Index 0 of the result is the intercept (bias). Returns (coefficients,
    final loss = negative log-likelihood + ridge penalty/2).
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
        nll = 0.0
        for row, yi in zip(Xn, y):
            z = w[0] + sum(wi * xi for wi, xi in zip(w[1:], row))
            p = sigmoid(z)
            err = p - yi
            grads[0] += err
            for j, xi in enumerate(row):
                grads[j + 1] += err * xi
            nll += -(yi * math.log(max(p, 1e-12)) + (1 - yi) * math.log(max(1 - p, 1e-12)))
        ridge_penalty = 0.0
        for j in range(1, k + 1):
            grads[j] += l2 * w[j]
            ridge_penalty += w[j] * w[j]
        for j in range(k + 1):
            w[j] -= lr * grads[j] / n
        loss_now = (nll + l2 * ridge_penalty / 2) / n
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


def _final_score(payload: dict[str, Any]) -> tuple[int, int]:
    """Final score from nested (Go) or flat (sample) frame shapes."""
    nested = payload.get("score")
    if isinstance(nested, dict):
        return int(nested.get("home", 0)), int(nested.get("away", 0))
    return int(payload.get("score_home", 0)), int(payload.get("score_away", 0))


def final_score(cycle: list[dict[str, Any]]) -> tuple[int, int] | None:
    """The declared final score that closes a cycle.

    Prefers an explicit FULLTIME marker; the mock ends at minute 90, so
    otherwise the highest-clock frame makes the final score. Only near-final
    frames (>= 88') count, so mid-run partial cycles are never mislabelled.
    """
    for payload in reversed(cycle):
        if str(payload.get("clock", "")).strip().upper() in ("FULLTIME", "FT"):
            return _final_score(payload)
    best = None
    for payload in cycle:
        minute = _parse_clock_minute(str(payload.get("clock", "")))
        if minute is None or minute < 88:
            continue
        if best is None or minute > best[0]:
            best = (minute, payload)
    if best is None:
        return None
    return _final_score(best[1])


def _frame_epoch(payload: dict[str, Any]) -> float | None:
    """Replay clock for a frame: received_at (ISO) falls back to `ts` (ms)."""
    received = payload.get("received_at")
    if received:
        try:
            dt = datetime.fromisoformat(str(received).replace("Z", "+00:00"))
            return dt.timestamp()
        except (ValueError, TypeError):
            pass
    ts = payload.get("ts")
    if ts is not None:
        try:
            return float(ts) / 1000.0
        except (TypeError, ValueError):
            pass
    return None


def collect_rows(
    cycles: list[list[dict[str, Any]]],
) -> tuple[list[list[float]], list[int], list[float], list[int], dict[str, int]]:
    """Build (x, y) training pairs mirroring live-model inputs exactly.

    Frames are replayed with their own receive timestamps so the study window
    folds exactly as it did live (this is what `asof` makes possible). Also
    returns the market-implied probability per row and per-cycle row counts
    (used for a chronological train/validation split).
    """
    X: list[list[float]] = []
    y: list[int] = []
    implied: list[float] = []
    cycle_sizes: list[int] = []
    report: dict[str, int] = {"cycles": 0, "frames": 0, "snapshots": 0}
    for cycle in cycles:
        score = final_score(cycle)
        if score is None or len(cycle) < 2:
            continue
        home, away = score
        report["cycles"] += 1
        report["frames"] += len(cycle)
        study = FeatureStudy()
        rows_before = len(X)
        for payload in cycle:
            asof = _frame_epoch(payload)
            for snap in study.observe_many(payload, asof=asof):
                snap.features = study.feature_vector(snap)
                for side in ("home", "away"):
                    odds = (snap.odds or {}).get(side)
                    X.append(FilterEngine.score_inputs(snap, side))
                    y.append(1 if _side_won(side, home, away) else 0)
                    implied.append(1.0 / odds if odds else 0.5)
                    report["snapshots"] += 1
        cycle_sizes.append(len(X) - rows_before)
    return X, y, implied, cycle_sizes, report


def _side_won(side: str, home: int, away: int) -> bool:
    if side == "home":
        return home > away
    if side == "away":
        return away > home
    return home == away


def metrics(X, y, weights, bias) -> tuple[float, float]:
    """Accuracy and Brier score of the fitted model on the given rows."""
    correct = 0
    brier = 0.0
    for row, yi in zip(X, y):
        z = bias + sum(w * xi for w, xi in zip(weights, row))
        p = sigmoid(z)
        correct += int((p >= 0.5) == bool(yi))
        brier += (p - yi) ** 2
    n = len(y)
    return (correct / n, brier / n) if n else (0.0, 0.0)


def _adoptable(rows: int, val_rows: int, weights: list[float], val_ll: float, base_ll: float) -> bool:
    """Adoption gate: refuse a signal-less fit that would silence trading.

    A calibration whose features carry no weight (all ~0) collapses every
    leg's posterior to a floor probability and blocks all executions — worse
    than the shim. Only adopt when there is real feature signal, enough
    history behind the fit, and the model actually beats the naive baseline.
    """
    if rows < 100 or val_rows < 20:
        return False
    if max(abs(w) for w in weights) < 1e-3:
        return False
    return val_ll < base_ll - 1e-9


def logloss(y: list[int], probs: list[float]) -> float:
    n = len(y)
    if n == 0:
        return float("inf")
    s = 0.0
    for yi, p in zip(y, probs):
        p = max(min(p, 1.0 - 1e-12), 1e-12)
        s += -(yi * math.log(p) + (1 - yi) * math.log(1 - p))
    return s / n


def baseline_logloss(y: list[int]) -> float:
    tau = fmean(y) if y else 0.0
    tau = max(min(tau, 1.0 - 1e-12), 1e-12)
    return -(tau * math.log(tau) + (1 - tau) * math.log(1 - tau))


def auc(y: list[int], probs: list[float]) -> float:
    """Mann-Whitney U AUC, O(n log n) via sort + average-rank tie-handling."""
    n = len(y)
    if n == 0:
        return 0.5
    n_pos = sum(y)
    n_neg = n - n_pos
    if n_pos == 0 or n_neg == 0:
        return 0.5
    order = sorted(range(n), key=lambda i: probs[i])
    ranks = [0.0] * n
    i = 0
    while i < n:
        j = i
        while j + 1 < n and probs[order[j + 1]] == probs[order[i]]:
            j += 1
        avg_rank = (i + j) / 2.0 + 1.0
        for q in range(i, j + 1):
            ranks[order[q]] = avg_rank
        i = j + 1
    rank_pos = sum(ranks[idx] for idx in range(n) if y[idx] == 1)
    u = rank_pos - n_pos * (n_pos + 1) / 2.0
    return u / (n_pos * n_neg)


def evaluation_report(
    X: list[list[float]],
    y: list[int],
    weights: list[float],
    bias: float,
    label: str,
) -> tuple[float, float, float]:
    """Print accuracy/Brier/logloss for a split and return (acc, brier, ll)."""
    acc, brier = metrics(X, y, weights, bias)
    probs = [sigmoid(bias + sum(w * xi for w, xi in zip(weights, row))) for row in X]
    ll = logloss(y, probs)
    bl = baseline_logloss(y)
    print(f"  {label:<7} rows={len(y):<5}  acc={acc:.3f}  brier={brier:.4f}  ll={ll:.4f}  base_ll={bl:.4f}")
    return acc, brier, ll


def decile_data(
    X: list[list[float]],
    y: list[int],
    weights: list[float],
    bias: float,
) -> list[list]:
    """Per-decile reliability bins for the dashboard (json-safe)."""
    buckets: dict[int, list[int]] = defaultdict(list)
    for row, yi in zip(X, y):
        p = sigmoid(bias + sum(w * xi for w, xi in zip(weights, row)))
        buckets[int(p * 10)].append(yi)
    out = []
    for decile in sorted(buckets):
        hits = buckets[decile]
        out.append([decile * 10, len(hits), round(fmean(hits), 4)])
    return out


def append_timeseries(
    *,
    status: str,
    rows: int,
    cycles: int,
    frames: int,
    l2: float,
    bias: float,
    weights: list[float],
    train_acc: float,
    train_brier: float,
    val_acc: float,
    val_brier: float,
    val_auc: float,
    base_ll: float,
    model_ll: float,
) -> None:
    """Append one drift-snapshot row per calibration run."""
    header = [
        "generated_at",
        "status",
        "rows",
        "cycles",
        "frames",
        "l2",
        "bias",
        *[f"w_{name}" for name in FEATURE_COLS],
        "train_accuracy",
        "train_brier",
        "val_accuracy",
        "val_brier",
        "val_auc",
        "baseline_logloss",
        "model_logloss",
    ]
    header_line = ",".join(header)
    rewrite = False
    if TIMESERIES_PATH.exists():
        with open(TIMESERIES_PATH, encoding="utf-8") as fh:
            existing_header = fh.readline().strip()
        if existing_header != header_line:
            rewrite = True
    else:
        rewrite = True

    # Write atomically: when the schema changed, rebuild the file through a
    # temp + replace so Windows file locks in other readers can't abort it.
    if rewrite:
        tmp = CALIB_DIR / ".timeseries.tmp"
        with open(tmp, "w", newline="", encoding="utf-8") as fh:
            csv.writer(fh).writerow(header)
        tmp.replace(TIMESERIES_PATH)
    with open(TIMESERIES_PATH, "a", newline="", encoding="utf-8") as fh:
        writer = csv.writer(fh)
        writer.writerow(
            [
                _now_iso(),
                status,
                rows,
                cycles,
                frames,
                l2,
                round(bias, 6),
                *(round(w, 6) for w in weights),
                round(train_acc, 4),
                round(train_brier, 4),
                round(val_acc, 4),
                round(val_brier, 4),
                round(val_auc, 4),
                round(base_ll, 4),
                round(model_ll, 4),
            ]
        )
    print(f"appended {TIMESERIES_PATH}")


def write_decay_matrix() -> None:
    """Exponential time-decay over the study window's 60s aggregation buckets.

    FeatureStudy._aggregate folds the sliding window into one bucket per
    minute; the decay matrix weights those buckets so freshest momentum
    dominates. tau is one third of the window (as seconds).
    """
    window_seconds = float(os.getenv("STUDY_WINDOW_SECONDS", "750"))
    tau = window_seconds / 3.0
    buckets = int(math.floor(window_seconds / 60.0))
    with open(DECAY_PATH, "w", newline="", encoding="utf-8") as fh:
        writer = csv.writer(fh)
        writer.writerow(["bucket_seconds", "bucket_minute", "weight"])
        for i in range(buckets + 1):
            start = i * 60
            minutes_ago = window_seconds - start
            weight = math.exp(-minutes_ago / tau)
            writer.writerow([start, start / 60.0, round(weight, 6)])
    print(f"wrote {DECAY_PATH}")


MINUTE_BANDS = [(0, 15), (15, 30), (30, 45), (45, 60), (60, 75), (75, 90)]


def _band_label(minute: float) -> str:
    if minute >= 90:
        return "75-90"
    for lo, hi in MINUTE_BANDS:
        if lo <= minute < hi:
            return f"{lo}-{hi}"
    return "0-15"


def write_season_trends(cycles: list[list[dict[str, Any]]]) -> None:
    """Historical trend dataset: per-league outcomes plus minute-band stats.

    One CSV rows: per league a FULL row with final-score outcome rates and one
    row per 15-minute band with observed danger/possession/shot baselines.
    """
    outcomes: dict[str, dict[str, list[int]]] = defaultdict(
        lambda: {"home_win": [], "draw": [], "away_win": [], "goals": []}
    )
    bands: dict[str, dict[str, dict[str, list[float]]]] = defaultdict(
        lambda: defaultdict(lambda: defaultdict(list))
    )
    for cycle in cycles:
        score = final_score(cycle)
        if score is None:
            continue
        home, away = score
        league = str(cycle[-1].get("league", "default"))
        outcomes[league]["home_win"].append(int(home > away))
        outcomes[league]["draw"].append(int(home == away))
        outcomes[league]["away_win"].append(int(home < away))
        outcomes[league]["goals"].append(home + away)
        for payload in cycle:
            minute = _parse_clock_minute(str(payload.get("clock", "")))
            if minute is None or minute <= 0:
                continue
            label = _band_label(minute)
            bucket = bands[league][label]
            bucket["danger_per_minute"].append(
                int(payload.get("dangerous_attacks", 0)) / minute
            )
            bucket["possession_home"].append(float(payload.get("possession_home", 0.5)))
            shots = int(payload.get("shots", 0))
            sot = int(payload.get("shots_on_target", 0))
            bucket["shot_accuracy"].append(sot / shots if shots else 0.0)

    with open(TRENDS_PATH, "w", newline="", encoding="utf-8") as fh:
        writer = csv.writer(fh)
        writer.writerow(
            [
                "league",
                "minute_band",
                "fields",
                "n",
                "home_win_rate",
                "draw_rate",
                "away_win_rate",
                "avg_goals",
                "danger_per_minute",
                "possession_home",
                "shot_accuracy",
            ]
        )
        for league in sorted(bands):
            for label, vals in sorted(
                bands[league].items(), key=lambda kv: int(kv[0].split("-")[0])
            ):
                writer.writerow(
                    [
                        league,
                        label,
                        "band",
                        len(vals["danger_per_minute"]),
                        "",
                        "",
                        "",
                        "",
                        round(fmean(vals["danger_per_minute"]), 3),
                        round(fmean(vals["possession_home"]), 3),
                        round(fmean(vals["shot_accuracy"]), 3),
                    ]
                )
            o = outcomes.get(league)
            if o and o["home_win"]:
                n = len(o["home_win"])
                writer.writerow(
                    [
                        league,
                        "FULL",
                        "outcomes",
                        n,
                        round(fmean(o["home_win"]), 3),
                        round(fmean(o["draw"]), 3),
                        round(fmean(o["away_win"]), 3),
                        round(fmean(o["goals"]), 3),
                        "",
                        "",
                        "",
                    ]
                )
    print(f"wrote {TRENDS_PATH}")


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


def _publish_model_hash(status: str, redis_url: str, fields: dict[str, Any] | None = None) -> None:
    """Mirror the run outcome into Redis for the web dashboard.

    Writes the `calibration:model` hash plus a short-lived `calibration:heartbeat`
    expiry so /api/stats can report freshness/staleness without scraping files.
    Field names match what the relay's /api/stats expects.
    """
    base = {
        "status": status,
        "generated_at": _now_iso(),
        "features": json.dumps(list(FEATURE_COLS)),
    }
    if fields:
        base.update({str(k): v for k, v in fields.items()})
    try:
        r = redis_sync.from_url(redis_url, decode_responses=True)
        weights = base.pop("weights", None)
        mapping = {
            k: (v if isinstance(v, str) else json.dumps(v))
            for k, v in base.items()
        }
        if weights:
            mapping["weights"] = "[" + ",".join(f"{round(w, 6)}" for w in weights) + "]"
        r.hset("calibration:model", mapping=mapping)
        r.set("calibration:heartbeat", _now_iso(), ex=3600)
        r.expire("calibration:model", 7 * 86400)
        r.close()
        print(f"published calibration:model status={status}")
    except Exception as exc:  # Redis offline must not abort the run
        print(f"  (redis publish skipped: {exc})")


def main() -> None:
    parser = argparse.ArgumentParser(description="Fit the Study Room win-probability model.")
    parser.add_argument("--redis-url", default=os.getenv("REDIS_URL", "redis://localhost:6379/0"))
    parser.add_argument("--frames", type=int, default=int(os.getenv("CALIBRATE_FRAMES", "2000")), help="max frames to read from history")
    parser.add_argument("--update-distributions", action="store_true", help="recompute baselines")
    parser.add_argument("--l2", type=float, default=0.001, help="L2 ridge penalty (default 0.001; sklearn.linear_model handles feature std internally so larger lambdas do NOT tame raw-scale feature magnitudes - use MODEL_TEMPERATURE / MAX_TRUE_PROB in filter_engine.py for that)")
    args = parser.parse_args()

    logging.basicConfig(level=logging.INFO, format="%(message)s")
    print(f"read: {args.frames} frames from stream {STREAM}")

    frames = load_frames(args.redis_url, args.frames)
    print(f"  -> {len(frames)} full frames")
    if len(frames) < 8:
        print("not enough history; let the mock run a few cycles first")
    cycles = group_cycles(frames)
    X, y, _implied, cycle_sizes, report = collect_rows(cycles)
    print(
        f"rows {len(X)} across {report['cycles']} complete cycles "
        f"({report['frames']} frames, {report['snapshots']} snapshots)"
    )
    if len(X) < 20:
        print("insufficient rows; skipping the fit (no weights written)")
        _publish_model_hash("insufficient", args.redis_url)
        try:
            write_decay_matrix()
            write_season_trends(cycles)
        except OSError as exc:
            print(f"decay/trends write skipped: {exc}")
        if args.update_distributions:
            dists = recompute_distributions(frames)
            DISTS_PATH.write_text(json.dumps(dists, indent=2, sort_keys=True), encoding="utf-8")
            print(f"wrote {DISTS_PATH}")
        return

    # Chronological train/validation split: first 80% of cycles → train.
    split_ci = max(1, int(len(cycle_sizes) * 0.8))
    train_end = sum(cycle_sizes[:split_ci])
    Xtr, ytr, _ = X[:train_end], y[:train_end], _implied[:train_end]
    Xva, yva, _ = X[train_end:], y[train_end:], _implied[train_end:]
    print(f"split: train={len(Xtr)}  val={len(Xva)}")

    biases_weights, loss = fit_logistic(Xtr, ytr, l2=args.l2)
    bias, weights = biases_weights[0], list(biases_weights[1:])
    print(f"fit ok  loss={loss:.4f}  l2={args.l2}")
    print(f"bias {bias:+.4f}")
    for name, w in zip(FEATURE_COLS, weights):
        print(f"  {name:<20} {w:+.4f}")

    train_acc, train_brier, train_ll = evaluation_report(Xtr, ytr, weights, bias, "train")
    val_acc, val_brier, val_ll = evaluation_report(Xva, yva, weights, bias, "val")
    val_probs = [sigmoid(bias + sum(w * xi for w, xi in zip(weights, row))) for row in Xva]
    val_auc_val = auc(yva, val_probs)
    base_ll = baseline_logloss(yva)
    print(f"  baseline ll={base_ll:.4f}  auc={val_auc_val:.4f}")

    adopt = _adoptable(
        rows=len(Xtr),
        val_rows=len(Xva),
        weights=weights,
        val_ll=val_ll,
        base_ll=base_ll,
    )
    try:
        append_timeseries(
            status="adopted" if adopt else "refused",
            rows=len(X),
            cycles=report["cycles"],
            frames=report["frames"],
            l2=args.l2,
            bias=bias,
            weights=weights,
            train_acc=train_acc,
            train_brier=train_brier,
            val_acc=val_acc,
            val_brier=val_brier,
            val_auc=val_auc_val,
            base_ll=base_ll,
            model_ll=val_ll,
        )
    except OSError as exc:
        print(f"timeseries append skipped: {exc}")

    if not adopt:
        print(
            "fit has no usable signal (draw-heavy history, too few rows, or "
            "model doesn't beat baseline); keeping the shim in force"
        )
        _publish_model_hash("refused", args.redis_url)
    else:
        payload = {
            "version": 3,
            "generated_at": _now_iso(),
            "train_rows": len(Xtr),
            "val_rows": len(Xva),
            "features": FEATURE_COLS,
            "l2": args.l2,
            "bias": round(bias, 6),
            "weights": [round(w, 6) for w in weights],
            "train_accuracy": round(train_acc, 4),
            "train_brier": round(train_brier, 4),
            "val_accuracy": round(val_acc, 4),
            "val_brier": round(val_brier, 4),
            "val_auc": round(val_auc_val, 4),
            "baseline_logloss": round(base_ll, 4),
            "model_logloss": round(val_ll, 4),
        }
        # Machine-readable line for one-shot containers / log capture.
        payload_compact = json.dumps(payload, sort_keys=True)
        try:
            WEIGHTS_PATH.write_text(
                json.dumps(payload, indent=2, sort_keys=True), encoding="utf-8"
            )
            print(f"wrote {WEIGHTS_PATH}")
        except OSError as exc:
            print(f"weights write skipped: {exc}")
        print(f"CALIB_JSON={payload_compact}")
        _publish_model_hash(
            "adopted",
            args.redis_url,
            fields={
                "rows": len(X),
                "train_rows": len(Xtr),
                "val_rows": len(Xva),
                "bias": round(bias, 6),
                "weights": weights,
                "l2": args.l2,
                "train_accuracy": round(train_acc, 4),
                "train_brier": round(train_brier, 4),
                "val_accuracy": round(val_acc, 4),
                "val_brier": round(val_brier, 4),
                "val_auc": round(val_auc_val, 4),
                "baseline_logloss": round(base_ll, 4),
                "model_logloss": round(val_ll, 4),
            },
        )

    try:
        write_decay_matrix()
        write_season_trends(cycles)
    except OSError as exc:
        print(f"decay/trends write skipped: {exc}")

    if args.update_distributions:
        try:
            dists = recompute_distributions(frames)
            DISTS_PATH.write_text(json.dumps(dists, indent=2, sort_keys=True), encoding="utf-8")
            print(f"wrote {DISTS_PATH} with leagues: {', '.join(dists['leagues']) if dists['leagues'] else '(none)'}")
        except OSError as exc:
            print(f"distributions write skipped: {exc}")


if __name__ == "__main__":
    main()