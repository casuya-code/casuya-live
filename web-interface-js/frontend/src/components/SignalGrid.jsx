"use client";

import { useState } from "react";

function copyPayload(signal) {
  const fixture = [signal.home_team, signal.away_team]
    .filter(Boolean)
    .join(" vs ") || signal.match_id;
  return [
    fixture,
    signal.market_label || `${signal.market} ${signal.side}`,
    `@${Number(signal.odds).toFixed(2)}`,
    `stake ${Number(signal.stake).toFixed(2)}`,
  ].join(" — ");
}

function CopyButton({ signal }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(copyPayload(signal));
      setCopied(true);
      setTimeout(() => setCopied(false), 1400);
    } catch {
      // clipboard unavailable
    }
  }

  return (
    <button
      type="button"
      onClick={copy}
      className="mono"
      style={{
        padding: "6px 10px",
        borderRadius: "8px",
        border: "1px solid var(--hairline)",
        background: copied ? "rgba(52,211,153,0.14)" : "var(--surface-2)",
        color: copied ? "var(--go)" : "var(--text-2)",
        fontSize: "11px",
        fontWeight: 700,
        letterSpacing: "0.06em",
        cursor: "pointer",
        whiteSpace: "nowrap",
        transition: "background .2s ease, color .2s ease",
      }}
    >
      {copied ? "COPIED" : "COPY MATCH"}
    </button>
  );
}

function timeAgo(ms) {
  if (!ms) return "";
  const s = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  return `${Math.floor(m / 60)}h ago`;
}

function PlaceButton({ onClick }) {
  const [busy, setBusy] = useState(false);

  async function place() {
    setBusy(true);
    try {
      await onClick();
    } finally {
      setBusy(false);
    }
  }

  return (
    <button
      type="button"
      onClick={place}
      disabled={busy}
      className="mono"
      title="Copy the ticket, place it on Helabet manually, then confirm here"
      style={{
        padding: "6px 10px",
        borderRadius: "8px",
        border: "1px solid var(--accent)",
        background: "rgba(139,92,246,0.14)",
        color: "var(--accent)",
        fontSize: "11px",
        fontWeight: 700,
        letterSpacing: "0.06em",
        cursor: busy ? "wait" : "pointer",
        whiteSpace: "nowrap",
        transition: "background .2s ease, color .2s ease",
      }}
    >
      {busy ? "RECORDING…" : "MARK PLACED"}
    </button>
  );
}

function SignalCard({ signal, flashing, placement, onPlace }) {
  const odds = Number(signal.odds);
  const raw = Number(signal.raw_odds);
  const stake = Number(signal.stake);
  const fixture = [signal.home_team, signal.away_team].filter(Boolean).join(" vs ");

  return (
    <article
      key={signal.order_id}
      className="card card--lift"
      style={{ padding: "14px 16px", ...(flashing ? { animation: "flash-up 1.5s ease" } : {}) }}
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
        <span className="eyebrow" style={{ maxWidth: "60%", overflow: "hidden", textOverflow: "ellipsis" }}>
          {signal.league || "signal"}
        </span>
        <span className="chip">
          <span className="live-dot" style={{ width: 6, height: 6 }} />
          {signal.clock ? `${signal.clock}'` : "live"}
        </span>
      </div>

      <div style={{ marginBottom: "8px", minHeight: "36px" }}>
        <div className="team-name" title={fixture} style={{ maxWidth: "100%", fontWeight: 800 }}>
          {fixture || signal.match_id}
        </div>
        <div className="eyebrow" style={{ marginTop: "2px", color: "var(--accent-2)" }}>
          {signal.market_label || `${signal.market} ${signal.side}`}
        </div>
      </div>

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "1fr auto",
          gap: "10px",
          alignItems: "center",
          marginBottom: "8px",
        }}
      >
        <div>
          <div className="mono" style={{ fontSize: "26px", fontWeight: 800, lineHeight: 1.1, color: "var(--text)" }}>
            {Number.isFinite(odds) ? odds.toFixed(2) : "—"}
          </div>
          {Number.isFinite(raw) && Math.abs(raw - odds) > 0.001 && (
            <div className="mono" style={{ fontSize: "11px", color: "var(--text-3)" }}>
              raw {raw.toFixed(2)}
            </div>
          )}
        </div>
        <div style={{ textAlign: "right" }}>
          <div className="mono" style={{ fontSize: "16px", fontWeight: 700, color: "var(--accent)" }}>
            stake {Number.isFinite(stake) ? stake.toFixed(2) : "—"}
          </div>
          {Number.isFinite(signal.true_prob) && (
            <div className="mono" style={{ fontSize: "11px", color: "var(--text-3)" }}>
              model p {(Number(signal.true_prob) * 100).toFixed(0)}%
            </div>
          )}
        </div>
      </div>

      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: "8px" }}>
        <span className="mono" style={{ fontSize: 10, color: "var(--text-3)", overflow: "hidden", textOverflow: "ellipsis" }}>
          {signal.order_id}
        </span>
        <div style={{ display: "flex", gap: "6px", alignItems: "center" }}>
          {placement ? (
            <span
              className="mono"
              style={{
                padding: "6px 10px",
                borderRadius: "8px",
                background:
                  placement.status === "void"
                    ? "rgba(142,162,192,0.14)"
                    : "rgba(52,211,153,0.14)",
                color: placement.status === "void" ? "var(--text-3)" : "var(--go)",
                fontSize: "11px",
                fontWeight: 700,
                letterSpacing: "0.06em",
                whiteSpace: "nowrap",
              }}
            >
              {placement.status === "placed" ? "PLACED" : placement.status.toUpperCase()} · {placement.book || "helabet"}
              {placement.placed_at ? ` · ${timeAgo(Number(placement.placed_at))}` : ""}
            </span>
          ) : (
            <PlaceButton onClick={() => onPlace && onPlace(signal)} />
          )}
        </div>
      </div>
    </article>
  );
}

export default function SignalGrid({ signals, flashId, connected, placements, onPlace }) {
  return (
    <section className="card" style={{ padding: "20px" }}>
      <div className="section-head">
        <div className="section-title">
          <span className="badge badge--live">zone a</span>
          <h2>LIVE SIGNAL QUEUE</h2>
          <span className="section-rule" />
        </div>
        <span className="chip">
          <span className={`live-dot ${connected ? "" : "live-dot--off"}`} />
          {connected ? "ENGINE ONLINE" : "ENGINE OFFLINE"}
        </span>
      </div>

      {signals.length === 0 ? (
        <div className="empty">
          {connected
            ? "Waiting for the engine to place the next virtual bet…"
            : "Connecting to the execution engine…"}
        </div>
      ) : (
        <>
          <div className="card-grid" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(min(100%, 300px), 1fr))" }}>
            {signals.map((s) => (
              <SignalCard
                key={s.order_id}
                signal={s}
                flashing={flashId === s.order_id}
                placement={placements?.[s.order_id]}
                onPlace={onPlace}
              />
            ))}
          </div>
          <footer className="page-foot" style={{ marginTop: "16px", paddingTop: "12px", borderTop: "1px solid var(--hairline)" }}>
            <span>market:signals:live · engine stream · operator-assisted live</span>
            <span className="mono">{signals.length} in queue</span>
          </footer>
        </>
      )}
    </section>
  );
}