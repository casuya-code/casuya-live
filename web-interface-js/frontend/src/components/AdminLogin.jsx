"use client";

import { useState } from "react";
import { apiBase, saveSessionToken } from "../lib/session";

/**
 * Admin login gate. Exchanges the pre-shared ADMIN_SECRET for a short-lived
 * session token at POST /api/auth/token, then stores it so the admin
 * dashboard's WebSocket and /api/stats fetches are authenticated.
 */
export default function AdminLogin({ onLogin }) {
  const [secret, setSecret] = useState("");
  const [error, setError] = useState(null);
  const [busy, setBusy] = useState(false);

  async function submit(e) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await fetch(`${apiBase()}/api/auth/token`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ secret }),
      });
      const body = await res.json();
      if (!res.ok) throw new Error(body.message || `token ${res.status}`);
      saveSessionToken(body.token, body.expires_at);
      onLogin(body.token);
    } catch (err) {
      setError(err.message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="card" style={{ maxWidth: "420px", margin: "0 auto", padding: "24px" }}>
      <div className="section-title" style={{ marginBottom: "14px" }}>
        <span className="eyebrow">admin tier</span>
        <h2 style={{ fontSize: "20px" }}>OPERATOR LOGIN</h2>
      </div>
      <p className="empty" style={{ textAlign: "left", marginBottom: "16px", padding: 0 }}>
        Enter the relay&apos;s pre-shared <code>ADMIN_SECRET</code> to issue a session
        token. Public dashboard clients do <em>not</em> need this — they see the
        live grid only.
      </p>
      <form onSubmit={submit} style={{ display: "flex", flexDirection: "column", gap: "10px" }}>
        <input
          type="password"
          value={secret}
          onChange={(e) => setSecret(e.target.value)}
          placeholder="ADMIN_SECRET"
          autoComplete="off"
          spellCheck="false"
          style={{
            padding: "10px 12px",
            borderRadius: "8px",
            border: "1px solid var(--hairline)",
            background: "var(--panel)",
            color: "var(--text-1)",
            fontFamily: "var(--mono)",
          }}
        />
        {error && <div style={{ color: "var(--danger)", fontSize: "13px" }}>{error}</div>}
        <button
          type="submit"
          disabled={busy || !secret}
          className="nav-meta"
          style={{
            padding: "10px 14px",
            borderRadius: "8px",
            border: "1px solid rgba(52,211,153,0.4)",
            background: "rgba(52,211,153,0.12)",
            color: "var(--go)",
            fontWeight: 700,
            cursor: "pointer",
          }}
        >
          {busy ? "Issuing session…" : "Unlock admin dashboard"}
        </button>
      </form>
    </section>
  );
}