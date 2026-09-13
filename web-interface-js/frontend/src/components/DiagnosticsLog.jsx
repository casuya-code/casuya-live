"use client";

function severityClass(drift) {
  const v = Number(drift);
  if (!Number.isFinite(v)) return "ok";
  if (v >= 0.3) return "danger";
  if (v >= 0.15) return "warn";
  return "ok";
}

function severityLabel(drift) {
  const v = Number(drift);
  if (!Number.isFinite(v)) return { label: "OK", cls: "ok" };
  if (v >= 0.3) return { label: "HIGH DRIFT", cls: "danger" };
  if (v >= 0.15) return { label: "MODERATE", cls: "warn" };
  return { label: "LOW", cls: "ok" };
}

export default function DiagnosticsLog({ entries }) {
  return (
    <section className="card" style={{ padding: "20px", marginTop: "18px" }}>
      <div className="section-head">
        <div className="section-title">
          <span className="badge badge--danger" style={{ background: "rgba(248,113,113,0.12)", color: "var(--danger)" }}>
            ai
          </span>
          <h2>DIAGNOSTICS — AI SELF-CORRECTION</h2>
          <span className="section-rule" />
        </div>
        <span className="chip">{entries.length} audit{entries.length === 1 ? "" : "s"}</span>
      </div>

      {entries.length === 0 ? (
        <div className="empty">No LOST-bet drift audits in this session.</div>
      ) : (
        <div className="diag-grid">
          {entries.map((entry, idx) => {
            const sev = severityClass(entry.drift_score);
            const sevInfo = severityLabel(entry.drift_score);
            return (
              <article
                key={`${entry.match_id}-${idx}`}
                className="card card--lift"
                style={{ padding: "18px" }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "center",
                    gap: "10px",
                    marginBottom: "10px",
                    flexWrap: "wrap",
                  }}
                >
                  <div className="audit audit--danger" style={{ marginLeft: -18, paddingLeft: 16, borderLeft: `3px solid var(--${sev === "ok" ? "go" : sev === "warn" ? "warn" : "danger"})` }}>
                    <span className="eyebrow" style={{ display: "block", marginBottom: "2px" }}>
                      {entry.match_id}
                    </span>
                    <strong style={{ fontSize: "22px" }}>
                      {entry.final_home}–{entry.final_away}
                    </strong>
                  </div>
                  <span className={`badge badge--${sevInfo.cls}`} style={sevInfo.cls === "ok" ? { borderColor: "rgba(52,211,153,0.35)", color: "var(--go)", background: "rgba(52,211,153,0.12)" } : sevInfo.cls === "warn" ? { borderColor: "rgba(251,191,36,0.35)", color: "var(--warn)", background: "rgba(251,191,36,0.12)" } : { borderColor: "rgba(248,113,113,0.35)", color: "var(--danger)", background: "rgba(248,113,113,0.12)" }}>
                    {sevInfo.label}
                  </span>
                </div>

                <div
                  className="nav-meta"
                  style={{
                    gap: "8px",
                    marginBottom: "12px",
                    flexWrap: "wrap",
                  }}
                >
                  <span className="chip">drift {Number(entry.drift_score).toFixed(4)}</span>
                  <span className="chip">predicted {entry.predicted_side}</span>
                </div>

                {entry.recommendations && entry.recommendations.length > 0 && (
                  <ul className="rec-list">
                    {entry.recommendations.map((rec, i) => (
                      <li key={i}>{rec}</li>
                    ))}
                  </ul>
                )}
              </article>
            );
          })}
        </div>
      )}
    </section>
  );
}