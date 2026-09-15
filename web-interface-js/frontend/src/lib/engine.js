"use client";

const ENGINE_URL =
  process.env.NEXT_PUBLIC_ENGINE_URL || "http://localhost:8081";

/**
 * Base URL of the execution engine's signal API. Points at the Go executor
 * (Railway) directly; configured via NEXT_PUBLIC_ENGINE_URL.
 */
export function engineBase() {
  return ENGINE_URL.replace(/\/$/, "");
}

/**
 * WebSocket endpoint on the engine that streams live signals. The Go handler
 * serves upgrades on /api/v1/stream/signals.
 */
export function engineWsUrl() {
  try {
    const parsed = new URL(engineBase());
    parsed.protocol = parsed.protocol === "http:" ? "ws:" : "wss:";
    parsed.pathname = "/api/v1/stream/signals";
    parsed.search = "";
    parsed.hash = "";
    return parsed.toString();
  } catch {
    return "ws://localhost:8081/api/v1/stream/signals";
  }
}

async function getJson(path) {
  const res = await fetch(`${engineBase()}${path}`, { cache: "no-store" });
  if (!res.ok) throw new Error(`${path} ${res.status}`);
  return res.json();
}

async function postJson(path, body) {
  const res = await fetch(`${engineBase()}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    cache: "no-store",
  });
  if (!res.ok) throw new Error(`${path} ${res.status}`);
  return res.json();
}

export function fetchActiveSignals() {
  return getJson("/api/v1/signals/active");
}

export function fetchPnl() {
  return getJson("/api/v1/analytics/pnl");
}

export function fetchLedger(limit = 50) {
  return getJson(`/api/v1/analytics/ledger?limit=${limit}`);
}

export function fetchPlacements(limit = 50) {
  return getJson(`/api/v1/operator/placements?limit=${limit}`);
}

export function recordPlacement(payload) {
  return postJson("/api/v1/operator/place", payload);
}