# CASUYA-LIVE analytics baseline repository.
#
# This directory holds the predictive baseline datasets referenced by
# feature_study.py. Each file is generated during offline calibration and
# shipped to this repository so the online Study Room has deterministic
# expectations to compare against live momentum.

# distributions.json         -> per-league shot/danger/possession baselines
# decay_matrix.npz           -> time-decay weight matrix for momentum windows
# season_trends.csv          -> historical trend datasets per market scope
# calibration_timeseries.csv -> historical calibration drift snapshots