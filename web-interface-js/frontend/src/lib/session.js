"use client";

const WS_URL =
  process.env.NEXT_PUBLIC_WS_BACKEND_URL || "ws://localhost:8080";
const STORAGE_KEY = "casuya_admin_session";

/**
 * Derive the relay HTTP API base from the WebSocket URL so one env var
 * configures both transports (ws://relay → http://relay, adds /ws path for
 * the WebSocket itself).
 */
export function apiBase() {
  try {
    const parsed = new URL(WS_URL.includes("://") ? WS_URL : `wss://${WS_URL}`);
    parsed.protocol = parsed.protocol === "wss:" ? "https:" : "http:";
    parsed.search = "";
    parsed.hash = "";
    return parsed.toString().replace(/\/$/, "");
  } catch {
    return "http://localhost:8080";
  }
}

/**
 * Resolve a relay WebSocket URL, appending an admin session token when one
 * is held. The relay upgrades public vs admin sockets based on the token at
 * handshake time.
 */
export function resolveWsUrl() {
  const base = WS_URL.startsWith("ws://") || WS_URL.startsWith("wss://")
    ? WS_URL
    : `wss://${WS_URL}`;
  let parsed;
  try {
    parsed = new URL(base);
    if (parsed.pathname === "/" || parsed.pathname === "") {
      parsed.pathname = "/ws";
    }
  } catch {
    parsed = new URL(
      (base.replace(/\/$/, "") + "/ws").replace(/^wss:/, "https:").replace(/^ws:/, "http:")
    );
  }
  const token = getSessionToken();
  if (token) parsed.searchParams.set("token", token);
  return parsed.toString();
}

export function getSessionToken() {
  if (typeof localStorage === "undefined") return null;
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const session = JSON.parse(raw);
    if (!session?.token || (session.expires_at * 1000) <= Date.now()) {
      localStorage.removeItem(STORAGE_KEY);
      return null;
    }
    return session.token;
  } catch {
    return null;
  }
}

export function saveSessionToken(token, expiresAt) {
  if (typeof localStorage === "undefined") return;
  localStorage.setItem(
    STORAGE_KEY,
    JSON.stringify({ token, expires_at: expiresAt, saved_at: Date.now() })
  );
}

export function clearSessionToken() {
  if (typeof localStorage === "undefined") return;
  localStorage.removeItem(STORAGE_KEY);
}