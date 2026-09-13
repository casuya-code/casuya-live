"use client";

import { useEffect, useRef, useState } from "react";

const WS_URL =
  process.env.NEXT_PUBLIC_WS_BACKEND_URL || "ws://localhost:8080";

/**
 * Resolve the relay WebSocket URL. The backend serves upgrades on /ws only,
 * so a bare host (e.g. wss://api-casuya-live.up.railway.app) must gain the
 * path — otherwise the handshake 404s and the dashboard stays offline.
 */
function resolveUrl(raw) {
  const base =
    raw.startsWith("ws://") || raw.startsWith("wss://")
      ? raw
      : `wss://${raw}`;
  try {
    const parsed = new URL(base);
    if (parsed.pathname === "/" || parsed.pathname === "") {
      parsed.pathname = "/ws";
    }
    return parsed.toString();
  } catch {
    return base.replace(/\/$/, "") + "/ws";
  }
}

/**
 * Maintains a resilient WebSocket connection to the Railway web relay and
 * distributes realtime payloads into type-scoped dashboard state.
 */
export default function useWebSockets() {
  const [connected, setConnected] = useState(false);
  const [lastEvent, setLastEvent] = useState(null);
  const [matches, setMatches] = useState([]);
  const [diagnostics, setDiagnostics] = useState([]);

  const socketRef = useRef(null);
  const retryRef = useRef(0);
  const timerRef = useRef(null);

  useEffect(() => {
    let disposed = false;

    function connect() {
      if (disposed) return;
      const socket = new WebSocket(resolveUrl(WS_URL));

      socket.onopen = () => {
        if (disposed) return;
        setConnected(true);
        retryRef.current = 0;
      };

      socket.onmessage = (event) => {
        if (disposed) return;
        try {
          const frame = JSON.parse(event.data);
          setLastEvent(frame);
          if (
            frame.type === "matches:live" &&
            frame.data &&
            frame.data.match_id
          ) {
            setMatches((prev) => {
              const next = prev.filter((m) => m.match_id !== frame.data.match_id);
              return [frame.data, ...next].slice(0, 40);
            });
          } else if (
            frame.type === "diagnostics:events" &&
            frame.data &&
            frame.data.match_id
          ) {
            setDiagnostics((prev) => [frame.data, ...prev].slice(0, 60));
          }
          // Other channel types (e.g. execution acks) are intentionally
          // ignored: they carry no dashboard state.
        } catch {
          // ignore malformed frames
        }
      };

      socket.onerror = () => {
        socket.close();
      };

      socket.onclose = () => {
        if (disposed) return;
        setConnected(false);
        retryRef.current += 1;
        const delay = Math.min(1000 * retryRef.current, 15000);
        timerRef.current = setTimeout(connect, delay);
      };

      socketRef.current = socket;
    }

    connect();
    return () => {
      disposed = true;
      if (timerRef.current) clearTimeout(timerRef.current);
      socketRef.current?.close();
    };
  }, []);

  return { connected, lastEvent, matches, diagnostics };
}