"use client";

import { useEffect, useRef, useState } from "react";
import { apiBase } from "../lib/session";

const STATS_URL = `${apiBase()}/api/stats`;

function statusBadge(status) {
  switch (status) {
    case "adopted":
      return <span className="badge badge--ok">ADOPTED</span>;
    case "refused":
      return <span className="badge badge--warn">REFUSED</span>;
    case "insufficient":
      return <span className="badge badge--danger">INSUFFICIENT</span>;
    default:
      return <span className="badge badge--warn">{status || "NO FIT"}</span>;
  }
}

function pct(value) {
  const n = Number(value);
  return Number.isFinite(n) ? `${(n * 100).toFixed(1)}%` : "—";
}

function Metric({ label, value, accent, hint }) {
  return (
    <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
      <span className="eyebrow">{label}</span>
      <div
        className="mono"
        style={{ fontSize: "22px", fontWeight: 700, lineHeight: 1.2, ...(accent ? { color: accent } : {}) }}
      >
        {value}
      </div>
      {hint && (
        <div style={{ fontSize: "11px", color: "var(--text-3)", marginTop: "2px" }}>{hint}</div>
      )}
    </div>
  );
}

/**
 * Model accuracy + operator telemetry panel, fed by the relay's /api/stats.
 * Shows fit health (train/val, AUC, logloss vs baseline), adopted feature
 * weights, accumulated outcomes, and per-channel stream staleness.
 *
 * Accepts an optional `token` prop. When provided, every stats request is
 * sent with the corresponding Authorization: Bearer header so the relay
 * serves the full admin-tier response body.
 */
export default function ModelAccuracyPanel({ token }) {
  const [stats, setStats] = useState(null);
  const [error, setError] = useState(null);
  const timerRef = useRef(null);

  useEffect(() => {
    let disposed = false;

    async function poll() {
      if (!token) {
        if (!disposed) setError("no admin session");
        return;
      }
      try {
        const res = await fetch(STATS_URL, {
          headers: { Authorization: `Bearer ${token}` },
        });
        if (!res.ok) throw new Error(`stats ${res.status}`);
        const body = await res.json();
        if (!disposed) {
          setStats(body);
          setError(null);
        }
      } catch (err) {
        if (!disposed) setError(err.message);
      }
    }

    poll();
    timerRef.current = setInterval(poll, 10000);
    return () => {
      disposed = true;
      clearInterval(timerRef.current);
    };
  }, [token]);

  const model = stats?.model;
  const staleSec = model?.status_age_seconds;

  return (
    <section className="card" style={{ padding: "20px", marginTop: "18px" }}>
      <div className="section-head">
        <div className="section-title">
          <span className="badge badge--value" style={{ opacity: 0.9 }}>
            model
          </span>
          <h2>MODEL ACCURACY — CALIBRATION</h2>
          <span className="section-rule" />
        </div>
        <div style={{ display: "flex", gap: "8px", alignItems: "center", flexWrap: "wrap" }}>
          {statusBadge(model?.status)}
          {stats && (
            <span className="chip">
              {staleSec === null ? "no fit timestamp" : staleSec < 900 ? `${staleSec}s old` : `stale ${Math.floor(staleSec / 60)}m`}
            </span>
          )}
        </div>
      </div>

      {error && !stats ? (
        <div className="empty">Can&apos;t reach relay stats: {error}</div>
      ) : !stats ? (
        <div className="empty">Loading model stats…</div>
      ) : (
        <>
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 150px), 1fr))",
              gap: "12px",
              marginBottom: "18px",
            }}
          >
            <Metric label="train accuracy" value={pct(model.train_accuracy)} accent="var(--accent)" />
            <Metric label="validation accuracy" value={pct(model.val_accuracy)} accent="var(--go)" />
            <Metric label="validation AUC" value={Number.isFinite(Number(model.val_auc)) ? Number(model.val_auc).toFixed(3) : "—"} />
            <Metric
              label="logloss vs baseline"
              value={
                Number.isFinite(Number(model.model_logloss)) &&
                Number.isFinite(Number(model.baseline_logloss))
                  ? `${Number(model.model_logloss).toFixed(3)} / ${Number(model.baseline_logloss).toFixed(3)}`
                  : "—"
              }
              hint={Number(model.model_logloss) < Number(model.baseline_logloss) ? "beats baseline" : "warning"}
              accent={
                Number(model.model_logloss) < Number(model.baseline_logloss)
                  ? "var(--go)"
                  : "var(--danger)"
              }
            />
            <Metric label="train / val rows" value={`${model.train_rows ?? "—"} / ${model.val_rows ?? "—"}`} hint={model.l2 ? `l2 ${model.l2}` : undefined} />
            <Metric label="bias" value={Number.isFinite(Number(model.bias)) ? Number(model.bias).toFixed(3) : "—"} />
          </div>

          {Array.isArray(model.weight_names) && model.weight_names.length > 0 && (
            <div className="card" style={{ padding: "14px 16px", borderRadius: "12px", marginBottom: "18px" }}>
              <span className="eyebrow">fitted feature weights</span>
              <div style={{ display: "flex", flexDirection: "column", gap: "8px", marginTop: "8px" }}>
                {model.weight_names.map(({ name, weight }) => {
                  const w = Number(weight) || 0;
                  const width = Math.min(100, Math.abs(w) * 140);
                  const pos = w >= 0;
                  return (
                    <div key={name} style={{ display: "flex", alignItems: "center", gap: "10px" }}>
                      <span className="eyebrow" style={{ width: "150px", flex: "none", textAlign: "right" }}>
                        {name}
                      </span>
                      <div
                        style={{
                          flex: 1,
                          height: "10px",
                          background: "var(--panel)",
                          borderRadius: "5px",
                          position: "relative",
                          overflow: "hidden",
                        }}
                      >
                        <div
                          style={{
                            position: "absolute",
                            top: 0,
                            bottom: 0,
                            left: pos ? "50%" : `${50 - width}%`,
                            width: `${width}%`,
                            borderRadius: "5px",
                            background: pos ? "var(--go)" : "var(--danger)",
                            opacity: 0.85,
                          }}
                        />
                      </div>
                      <span className="mono" style={{ width: "64px", flex: "none", fontSize: "12px", color: "var(--text-2)" }}>
                        {w.toFixed(2)}
                      </span>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 220px), 1fr))",
              gap: "12px",
            }}
          >
            <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
              <span className="eyebrow">relay outcomes ledger</span>
              <div className="mono" style={{ fontSize: "24px", fontWeight: 700 }}>
                <span style={{ color: "var(--go)" }}>{stats.outcomes?.won || 0}</span>
                <span style={{ color: "var(--text-3)" }}> / </span>
                <span style={{ color: "var(--danger)" }}>{stats.outcomes?.lost || 0}</span>
                {"  "}
                <span style={{ fontSize: "13px", color: "var(--text-3)", marginLeft: "10px" }}>net {Number(stats.outcomes?.net || 0).toFixed(2)}</span>
              </div>
            </div>
            <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
              <span className="eyebrow">session bankroll (pnl:session)</span>
              <div className="mono" style={{ fontSize: "24px", fontWeight: 700 }}>
                <span style={{ color: Number(stats.pnl?.net) >= 0 ? "var(--go)" : "var(--danger)" }}>
                  {Number(stats.pnl?.net || 0).toFixed(2)}
                </span>
                {"  "}
                <span style={{ fontSize: "13px", color: "var(--text-3)", marginLeft: "10px" }}>
                  cycle {stats.pnl?.updated_cycle ?? "—"}
                </span>
              </div>
            </div>
            <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
              <span className="eyebrow">stream staleness</span>
              <div style={{ display: "flex", flexDirection: "column", gap: "4px", marginTop: "4px" }}>
                {Object.entries(stats.channels || {}).map(([ch, info]) => {
                  const age = info?.age_seconds;
                  const hot = age !== null && age < 60;
                  return (
                    <div key={ch} style={{ display: "flex", justifyContent: "space-between", gap: "8px", fontSize: "12px" }}>
                      <span className="mono" style={{ color: "var(--text-2)", overflow: "hidden", textOverflow: "ellipsis" }}>
                        {ch}
                      </span>
                      <span className={`mono`} style={{ color: hot ? "var(--go)" : "var(--danger)", whiteSpace: "nowrap" }}>
                        {age === null ? "silent" : `${age}s`}
                      </span>
                    </div>
                  );
                })}
              </div>
            </div>
          </div>

          <div className="page-foot" style={{ marginTop: "16px", paddingTop: "12px", borderTop: "1px solid var(--hairline)" }}>
            <span>calibration:model · relay /api/stats</span>
            <span className="mono">{model.generated_at ? new Date(model.generated_at).toLocaleString() : "fit not yet run"}</span>
          </div>
        </>
      )}
    </section>
  );
}