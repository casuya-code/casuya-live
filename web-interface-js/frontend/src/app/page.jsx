"use client";

import LiveBetsGrid from "../components/LiveBetsGrid";
import DiagnosticsLog from "../components/DiagnosticsLog";
import useWebSockets from "../hooks/useWebSockets";

export default function Page() {
  const { connected, lastEvent, matches, diagnostics } = useWebSockets();

  return (
    <main style={{ padding: "24px", maxWidth: "1280px", margin: "0 auto" }}>
      <header
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          marginBottom: "20px",
        }}
      >
        <div>
          <h1 style={{ margin: 0, fontSize: "22px", letterSpacing: "0.5px" }}>
            CASUYA-LIVE
          </h1>
          <p style={{ margin: "4px 0 0", color: "#8b949e", fontSize: "13px" }}>
            High-odds value detection — live operations
          </p>
        </div>
        <div style={{ display: "flex", gap: "12px", alignItems: "center" }}>
          <span
            style={{
              width: "10px",
              height: "10px",
              borderRadius: "50%",
              backgroundColor: connected ? "#3fb950" : "#f85149",
              display: "inline-block",
            }}
          />
          <span style={{ fontSize: "13px", color: "#8b949e" }}>
            {connected ? "RELAY ONLINE" : "RELAY OFFLINE"}
          </span>
          <span style={{ fontSize: "13px", color: "#8b949e" }}>
            last event{" "}
            {lastEvent
              ? new Date(lastEvent.ts).toLocaleTimeString()
              : "—"}
          </span>
        </div>
      </header>

      <LiveBetsGrid matches={matches} lastEvent={lastEvent} />
      <DiagnosticsLog entries={diagnostics} />
    </main>
  );
}