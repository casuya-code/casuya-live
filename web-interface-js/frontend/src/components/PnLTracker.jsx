"use client";

function signed(val, decimals = 2) {
  const n = Number(val);
  if (!Number.isFinite(n)) return "—";
  const sign = n > 0 ? "+" : n < 0 ? "" : "";
  return `${sign}${n.toFixed(decimals)}`;
}

function scoreOrDash(val) {
  const n = Number(val);
  return Number.isInteger(n) && n >= 0 ? n : "–";
}

function maxDrawdown(entries) {
  // entries arrive newest-first; walk oldest→newest to build the equity curve.
  let peak = 0;
  let curve = 0;
  let dd = 0;
  for (let i = entries.length - 1; i >= 0; i--) {
    curve += Number(entries[i].pnl) || 0;
    if (curve > peak) peak = curve;
    const drop = peak - curve;
    if (drop > dd) dd = drop;
  }
  return dd;
}

function ResultBadge({ result }) {
  if (result === "won") return <span className="badge badge--ok">WON</span>;
  if (result === "lost") return <span className="badge badge--danger">LOST</span>;
  return <span className="badge badge--warn">VOID</span>;
}

function timeAgo(ms) {
  const n = Number(ms);
  if (!Number.isFinite(n) || n <= 0) return "—";
  const sec = Math.max(0, Math.floor((Date.now() - n) / 1000));
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  return `${Math.floor(sec / 3600)}h ago`;
}

export default function PnLTracker({ pnl, ledger, loading }) {
  const net = Number(pnl?.net);
  const won = Number(pnl?.won) || 0;
  const lost = Number(pnl?.lost) || 0;
  const total = won + lost;
  const winRate = total > 0 ? Math.round((won / total) * 10000) / 100 : 0;
  const dd = maxDrawdown(ledger || []);
  const entries = ledger || [];

  return (
    <section className="card" style={{ padding: "20px" }}>
      <div className="section-head">
        <div className="section-title">
          <span className="badge badge--value" style={{ opacity: 0.9 }}>
            zone b
          </span>
          <h2>REAL-TIME P&L TRACKER</h2>
          <span className="section-rule" />
        </div>
        <span className="chip">{total} paper bets</span>
      </div>

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "repeat(auto-fit, minmax(min(100%, 160px), 1fr))",
          gap: "12px",
          marginBottom: "18px",
        }}
      >
        <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
          <span className="eyebrow">total paper bets</span>
          <div className="mono" style={{ fontSize: "30px", fontWeight: 800, lineHeight: 1.1 }}>
            {total}
          </div>
          <div style={{ fontSize: "11px", color: "var(--text-3)", marginTop: "2px" }}>
            {won} won · {lost} lost
          </div>
        </div>
        <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
          <span className="eyebrow">win rate</span>
          <div className="mono" style={{ fontSize: "30px", fontWeight: 800, lineHeight: 1.1, color: "var(--accent)" }}>
            {total > 0 ? `${winRate.toFixed(1)}%` : "—"}
          </div>
        </div>
        <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
          <span className="eyebrow">net profit (TZS)</span>
          <div
            className="mono"
            style={{ fontSize: "26px", fontWeight: 800, lineHeight: 1.1, color: net >= 0 ? "var(--go)" : "var(--danger)" }}
          >
            {Number.isFinite(net) ? signed(net) : "—"}
          </div>
        </div>
        <div className="card" style={{ padding: "14px 16px", borderRadius: "12px" }}>
          <span className="eyebrow">max drawdown</span>
          <div
            className="mono"
            style={{ fontSize: "26px", fontWeight: 800, lineHeight: 1.1, color: dd > 0 ? "var(--danger)" : "var(--go)" }}
          >
            {loading && entries.length === 0 ? "—" : `${dd > 0 ? "-" : ""}${dd.toFixed(2)}`}
          </div>
          <div style={{ fontSize: "11px", color: "var(--text-3)", marginTop: "2px" }}>window {entries.length} settled</div>
        </div>
      </div>

      <div
        className="card"
        style={{ padding: "0", borderRadius: "12px", overflow: "hidden" }}
      >
        <div style={{ padding: "14px 16px", borderBottom: "1px solid var(--hairline)" }}>
          <span className="eyebrow">settled ledger · execution:paper_settlements</span>
        </div>
        {entries.length === 0 ? (
          <div className="empty" style={{ margin: "14px", border: "none", padding: "22px 12px" }}>
            {loading ? "Loading settled trades…" : "No settled paper trades yet — waits for the next FULLTIME grade."}
          </div>
        ) : (
          <div style={{ overflowX: "auto" }}>
            <table
              style={{
                width: "100%",
                borderCollapse: "collapse",
                fontSize: "12px",
                fontVariantNumeric: "tabular-nums",
              }}
            >
              <thead>
                <tr style={{ textAlign: "left", color: "var(--text-3)" }}>
                  <th style={{ padding: "10px 12px", fontWeight: 600, letterSpacing: "0.04em", textTransform: "uppercase", fontSize: "10px" }}>when</th>
                  <th style={{ padding: "10px 12px", fontWeight: 600, letterSpacing: "0.04em", textTransform: "uppercase", fontSize: "10px" }}>match</th>
                  <th style={{ padding: "10px 12px", fontWeight: 600, letterSpacing: "0.04em", textTransform: "uppercase", fontSize: "10px" }}>market</th>
                  <th style={{ padding: "10px 12px", fontWeight: 600, letterSpacing: "0.04em", textTransform: "uppercase", fontSize: "10px", textAlign: "right" }}>odds</th>
                  <th style={{ padding: "10px 12px", fontWeight: 600, letterSpacing: "0.04em", textTransform: "uppercase", fontSize: "10px", textAlign: "right" }}>stake</th>
                  <th style={{ padding: "10px 12px", fontWeight: 600, letterSpacing: "0.04em", textTransform: "uppercase", fontSize: "10px", textAlign: "right" }}>final</th>
                  <th style={{ padding: "10px 12px", fontWeight: 600, letterSpacing: "0.04em", textTransform: "uppercase", fontSize: "10px" }}>result</th>
                  <th style={{ padding: "10px 12px", fontWeight: 600, letterSpacing: "0.04em", textTransform: "uppercase", fontSize: "10px", textAlign: "right" }}>pnl</th>
                </tr>
              </thead>
              <tbody>
                {entries.slice(0, 30).map((e) => (
                  <tr key={e.id || e.order_id} style={{ borderTop: "1px solid var(--hairline)" }}>
                    <td style={{ padding: "10px 12px", color: "var(--text-3)", whiteSpace: "nowrap" }}>{timeAgo(e.settled_at)}</td>
                    <td className="mono" style={{ padding: "10px 12px", color: "var(--text-2)", maxWidth: "160px", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{e.match_id}</td>
                    <td style={{ padding: "10px 12px", color: "var(--text-2)", whiteSpace: "nowrap" }}>{e.market} {e.side}</td>
                    <td className="mono" style={{ padding: "10px 12px", textAlign: "right" }}>{Number(e.odds).toFixed(2)}</td>
                    <td className="mono" style={{ padding: "10px 12px", textAlign: "right" }}>{Number(e.stake).toFixed(2)}</td>
                    <td className="mono" style={{ padding: "10px 12px", textAlign: "right", color: "var(--text-2)" }}>
                      {scoreOrDash(e.score_home)}–{scoreOrDash(e.score_away)}
                    </td>
                    <td style={{ padding: "10px 12px" }}>
                      <ResultBadge result={e.result} />
                    </td>
                    <td
                      className="mono"
                      style={{
                        padding: "10px 12px",
                        textAlign: "right",
                        fontWeight: 700,
                        color: Number(e.pnl) >= 0 ? "var(--go)" : "var(--danger)",
                      }}
                    >
                      {signed(e.pnl)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <footer className="page-foot" style={{ marginTop: "16px", paddingTop: "12px", borderTop: "1px solid var(--hairline)" }}>
        <span>engine /api/v1/analytics · model 0.8709 AUC</span>
        <span className="mono">live consistency check</span>
      </footer>
    </section>
  );
}