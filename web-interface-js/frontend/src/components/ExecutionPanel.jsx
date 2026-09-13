"use client";

function signed(val, decimals = 2) {
  const n = Number(val);
  if (!Number.isFinite(n)) return "—";
  const sign = n > 0 ? "+" : n < 0 ? "" : "";
  return `${sign}${n.toFixed(decimals)}`;
}

function ResultBadge({ result }) {
  if (result === "won") return <span className="badge badge--ok">WON</span>;
  if (result === "lost") return <span className="badge badge--danger">LOST</span>;
  return <span className="badge badge--warn">VOID</span>;
}

/**
 * Operator view of the execution loop: graded order results and the running
 * session bankroll, fed by the executor's execution:pnl channel.
 */
export default function ExecutionPanel({ pnl }) {
  const last = pnl?.last || null;
  const history = pnl?.history || [];
  const session = last?.session;

  return (
    <section className="card" style={{ padding: "20px", marginTop: "18px" }}>
      <div className="section-head">
        <div className="section-title">
          <span className="badge badge--value" style={{ opacity: 0.9 }}>
            exec
          </span>
          <h2>EXECUTION — REALISED P&L</h2>
          <span className="section-rule" />
        </div>
        <span className="chip">
          {last ? `cycle ${last.cycle}` : "awaiting settlement"}
        </span>
      </div>

      {!last ? (
        <div className="empty">
          No settled orders yet — waits for the first FULLTIME settlement.
        </div>
      ) : (
        <>
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 160px), 1fr))",
              gap: "12px",
              marginBottom: "18px",
            }}
          >
            <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
              <span className="eyebrow">session net</span>
              <div
                className="mono"
                style={{
                  fontSize: "30px",
                  fontWeight: 800,
                  lineHeight: 1.1,
                  color: Number(session?.net) >= 0 ? "var(--go)" : "var(--danger)",
                }}
              >
                {session ? signed(session.net) : "—"}
              </div>
            </div>
            <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
              <span className="eyebrow">won / lost</span>
              <div className="mono" style={{ fontSize: "24px", fontWeight: 700, lineHeight: 1.2 }}>
                <span style={{ color: "var(--go)" }}>{session ? session.won : 0}</span>
                <span style={{ color: "var(--text-3)" }}> / </span>
                <span style={{ color: "var(--danger)" }}>{session ? session.lost : 0}</span>
              </div>
            </div>
            <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
              <span className="eyebrow">this cycle</span>
              <div className="mono" style={{ fontSize: "22px", fontWeight: 700, lineHeight: 1.2 }}>
                <span style={{ color: "var(--accent)" }}>{last.settled?.length || 0}</span>
                <span style={{ fontSize: "12px", color: "var(--text-3)" }}> orders</span>
              </div>
            </div>
          </div>

          <div
            style={{
              display: "flex",
              alignItems: "center",
              gap: "10px",
              flexWrap: "wrap",
              marginBottom: "12px",
            }}
          >
            <span className="eyebrow" style={{ color: "var(--text-2)" }}>
              {last.match_id}
            </span>
            <span className="chip mono">
              final {last.final_score?.home ?? "–"}–{last.final_score?.away ?? "–"}
            </span>
            {last.settled?.length === 0 && (
              <span className="chip">no orders in cycle</span>
            )}
          </div>

          <div className="card-grid" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(min(100%, 280px), 1fr))" }}>
            {(last.settled || []).map((o) => (
              <div
                key={o.order_id}
                className="card card--lift"
                style={{ padding: "13px 15px", borderRadius: "12px", animationDelay: "0s" }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "center",
                    gap: "10px",
                  }}
                >
                  <div>
                    <span className="eyebrow" style={{ display: "block" }}>
                      {o.market} · {o.side}
                    </span>
                    <span className="mono" style={{ fontSize: "12px", color: "var(--text-3)" }}>
                      @{Number(o.odds).toFixed(2)} · stake {Number(o.stake).toFixed(2)}
                    </span>
                  </div>
                  <div
                    style={{
                      display: "flex",
                      flexDirection: "column",
                      alignItems: "flex-end",
                      gap: "4px",
                    }}
                  >
                    <ResultBadge result={o.result} />
                    <span
                      className="mono"
                      style={{
                        fontWeight: 800,
                        fontSize: "15px",
                        color:
                          Number(o.pnl) >= 0
                            ? "var(--go)"
                            : "var(--danger)",
                      }}
                    >
                      {signed(o.pnl)}
                    </span>
                  </div>
                </div>
              </div>
            ))}
          </div>

          <div
            className="page-foot"
            style={{ marginTop: "16px", paddingTop: "12px", borderTop: "1px solid var(--hairline)" }}
          >
            <span>pnl:session · redis hash</span>
            <span className="mono">{history.length} settlement(s)</span>
          </div>
        </>
      )}
    </section>
  );
}