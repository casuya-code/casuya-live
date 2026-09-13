"use client";

import LiveBetsGrid from "../components/LiveBetsGrid";
import DiagnosticsLog from "../components/DiagnosticsLog";
import useWebSockets from "../hooks/useWebSockets";

export default function Page() {
  const { connected, lastEvent, matches, diagnostics } = useWebSockets();

  const marketCount = matches.reduce(
    (acc, m) => acc + Object.keys(m.markets || {}).length,
    0
  );
  const lastSeen = lastEvent
    ? new Date(lastEvent.ts).toLocaleTimeString()
    : null;

  return (
    <>
      <nav className="nav">
        <div className="nav-inner">
          <div className="brand">
            <span className="brand-mark">C</span>
            <div>
              <h1>CASUYA-LIVE</h1>
              <p className="tagline">High-odds value detection — live operations</p>
            </div>
          </div>
          <div className="nav-meta">
            <span className={`pill ${connected ? "mono" : "mono"}`}>
              <span className={`live-dot ${connected ? "" : "live-dot--off"}`} />
              {connected ? "RELAY ONLINE" : "RELAY OFFLINE"}
            </span>
            {lastSeen && (
              <span className="pill mono">
                RX&nbsp;{lastSeen}
              </span>
            )}
          </div>
        </div>
      </nav>

      <main className="container">
        <section className="card card--lift section-head" style={{ padding: "16px 18px" }}>
          <div className="section-title">
            <span className="eyebrow">Live grid</span>
            <h2 style={{ fontSize: "20px" }}>Study Room</h2>
          </div>
          <div className="nav-meta">
            <span className="chip">{matches.length} matches</span>
            <span className="chip">{marketCount} markets</span>
            <span className="chip">{diagnostics.length} audits</span>
          </div>
        </section>

        <LiveBetsGrid matches={matches} lastEvent={lastEvent} />
        <DiagnosticsLog entries={diagnostics} />

        <footer className="page-foot">
          <span>casuya-live · value-first trading</span>
          <span>last frame {lastEvent ? `${lastEvent.type}` : "—"}</span>
        </footer>
      </main>
    </>
  );
}