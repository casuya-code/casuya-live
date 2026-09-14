"use client";

/**
 * Admin-only log of execution:commands payloads. The relay relays the raw
 * signed envelope; this component decodes the base64url payload and renders
 * the underlying order fields for operator visibility.
 */

function decodePayload(envelope) {
  if (typeof envelope !== "string") return null;
  const [payloadB64] = envelope.split(".");
  if (!payloadB64) return null;
  try {
    // pad base64url → base64
    const padded = payloadB64.replace(/-/g, "+").replace(/_/g, "/");
    const paddedFixed = padded + "=".repeat((4 - (padded.length % 4)) % 4);
    return JSON.parse(atob(paddedFixed));
  } catch {
    return null;
  }
}

export default function CommandsLog({ commands }) {
  if (!commands.length) {
    return (
      <section className="card" style={{ padding: "20px", marginTop: "18px" }}>
        <div className="section-head">
          <div className="section-title">
            <span className="badge badge--warn" style={{ background: "rgba(251,191,36,0.12)", color: "var(--warn)" }}>
              cmd
            </span>
            <h2>EXECUTION COMMANDS</h2>
            <span className="section-rule" />
          </div>
        </div>
        <div className="empty">No command payloads observed in this session.</div>
      </section>
    );
  }

  return (
    <section className="card" style={{ padding: "20px", marginTop: "18px" }}>
      <div className="section-head">
        <div className="section-title">
          <span className="badge badge--warn" style={{ background: "rgba(251,191,36,0.12)", color: "var(--warn)" }}>
            cmd
          </span>
          <h2>EXECUTION COMMANDS</h2>
          <span className="section-rule" />
        </div>
        <span className="chip">{commands.length} emitted</span>
      </div>

      <div
        className="card-grid"
        style={{ gridTemplateColumns: "repeat(auto-fill, minmax(min(100%, 300px), 1fr))" }}
      >
        {commands.map((cmd, idx) => {
          const payload = decodePayload(cmd?.payload || cmd);
          return (
            <div
              key={`${cmd?.order_id || idx}`}
              className="card card--lift"
              style={{ padding: "14px 15px", borderRadius: "12px" }}
            >
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: "6px" }}>
                <span className="eyebrow">
                  {payload?.match_id || cmd?.match_id || "—"}
                </span>
                <span className="chip mono" style={{ fontSize: "11px", color: "var(--text-3)" }}>
                  {payload?.ts ? new Date(payload.ts).toLocaleTimeString() : "—"}
                </span>
              </div>
              <div style={{ display: "flex", gap: "6px", alignItems: "center", marginBottom: "4px" }}>
                <span className="badge badge--ok">{payload?.side || cmd?.side}</span>
                <span className="eyebrow">{payload?.market_id || cmd?.market_id}</span>
                <span className="mono" style={{ fontSize: "12px", color: "var(--text-2)" }}>
                  @{payload?.odds?.toFixed?.(2) ?? cmd?.odds}
                </span>
              </div>
              <div style={{ display: "flex", gap: "8px", fontSize: "11px", color: "var(--text-3)", fontFamily: "var(--mono)" }}>
                <span>stake {payload?.amount?.toFixed?.(2) ?? "—"}</span>
                <span>·</span>
                <span>true {(payload?.true_prob * 100)?.toFixed?.(1) ?? "—"}%</span>
                <span>implied {(payload?.implied_prob * 100)?.toFixed?.(1) ?? "—"}%</span>
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}