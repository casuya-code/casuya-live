"use client";

import { useEffect, useState } from "react";
import AdminLogin from "../../components/AdminLogin";
import CommandsLog from "../../components/CommandsLog";
import DiagnosticsLog from "../../components/DiagnosticsLog";
import ExecutionPanel from "../../components/ExecutionPanel";
import ModelAccuracyPanel from "../../components/ModelAccuracyPanel";
import useAdminWebSockets from "../../hooks/useAdminWebSockets";
import {
  getSessionToken,
  clearSessionToken,
} from "../../lib/session";

export default function AdminPage() {
  const [token, setToken] = useState(null);
  const admin = useAdminWebSockets(token);

  useEffect(() => {
    const existing = getSessionToken();
    if (existing) setToken(existing);
  }, []);

  function handleLogin(nextToken) {
    setToken(nextToken);
  }

  function logout() {
    clearSessionToken();
    setTimeout(() => setToken(null), 0);
  }

  if (!token) {
    return (
      <>
        <Nav onLogout={logout} authed={false} />
        <main className="container" style={{ paddingTop: "70px" }}>
          <AdminLogin onLogin={handleLogin} />
        </main>
      </>
    );
  }

  return (
    <>
      <Nav onLogout={logout} authed={true} />
      <main className="container">
        <section className="card card--lift section-head" style={{ padding: "16px 18px" }}>
          <div className="section-title">
            <span className="eyebrow">admin tier</span>
            <h2 style={{ fontSize: "20px" }}>Operator Console</h2>
          </div>
          <div className="nav-meta">
            <span className={`pill ${admin.connected ? "mono" : "mono"}`}>
              <span className={`live-dot ${admin.connected ? "" : "live-dot--off"}`} />
              {admin.connected ? "ADMIN WS ONLINE" : "ADMIN WS OFFLINE"}
            </span>
            <span className="chip">signed session active</span>
          </div>
        </section>

        <ExecutionPanel pnl={admin.pnl} />
        <ModelAccuracyPanel token={token} />
        <CommandsLog commands={admin.commands} />
        <DiagnosticsLog entries={admin.diagnostics} />

        <footer className="page-foot">
          <span>casuya-live · admin tier — not for public distribution</span>
          <span>{admin.commands.length + admin.diagnostics.length} admin events</span>
        </footer>
      </main>
    </>
  );
}

function Nav({ onLogout, authed }) {
  return (
    <nav className="nav">
      <div className="nav-inner">
        <div className="brand">
          <span className="brand-mark">C</span>
          <div>
            <h1>CASUYA-LIVE</h1>
            <p className="tagline">Admin console — operator only</p>
          </div>
        </div>
        <div className="nav-meta">
          <a
            href="/signals"
            className="pill mono"
            style={{
              color: "var(--text-3)",
              textDecoration: "none",
              border: "1px solid var(--hairline)",
              padding: "4px 10px",
              borderRadius: "8px",
            }}
          >
            SIGNALS
          </a>
          <a
            href="/"
            className="pill mono"
            style={{
              color: "var(--text-3)",
              textDecoration: "none",
              border: "1px solid var(--hairline)",
              padding: "4px 10px",
              borderRadius: "8px",
            }}
          >
            PUBLIC
          </a>
          {authed && (
            <button
              type="button"
              onClick={onLogout}
              className="pill mono"
              style={{
                color: "var(--danger)",
                background: "none",
                border: "1px solid var(--hairline)",
                padding: "4px 10px",
                borderRadius: "8px",
                cursor: "pointer",
              }}
            >
              LOG OUT
            </button>
          )}
        </div>
      </div>
    </nav>
  );
}