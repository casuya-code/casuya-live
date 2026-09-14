# CASUYA-LIVE analytics baseline repository.
#
# This directory holds the predictive baseline datasets referenced by
# feature_study.py. Each file is generated during offline calibration and
# shipped to this repository so the online Study Room has deterministic
# expectations to compare against live momentum.

# distributions.json         -> per-league shot/danger/possession baselines
# weights.json               -> fitted logistic weights (+bias) with train/val
#                              metrics and L2 penalty; written by
#                              scripts/calibrate.py, absent while the offline
#                              fit has no signal (shim stays in force)
# decay_matrix.csv           -> exponential time-decay weighting across the
#                              study window's 60s aggregation buckets
# season_trends.csv          -> historical trend dataset: per-league outcome
#                              rates and minute-band baselines
# calibration_timeseries.csv -> run-by-run drift snapshots (status, weights,
#                              train/val accuracy + Brier, val AUC, logloss vs
#                              baseline); one row appended per run, rebuilt
#                              atomically when the schema changes