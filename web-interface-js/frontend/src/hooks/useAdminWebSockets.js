"use client";

import { useEffect, useRef, useState } from "react";
import { resolveWsUrl } from "../lib/session";

/**
 * Admin-tier WebSocket hook. Upgrades with the admin session token so the
 * relay allocates the full channel set to this socket: PnL settlements,
 * diagnostics events, and the HMAC-signed execution:commands bus. Public
 * dashboards must NOT use this hook; the relay enforces the tiering, but the
 * hook additionally refuses to connect without a valid session.
 *
 * Pass the current session token so the effect re-establishes the socket
 * when a session starts (login) or expires (logout).
 */
export default function useAdminWebSockets(token) {
  const [connected, setConnected] = useState(false);
  const [lastEvent, setLastEvent] = useState(null);
  const [pnl, setPnl] = useState(null);
  const [diagnostics, setDiagnostics] = useState([]);
  const [commands, setCommands] = useState([]);

  const socketRef = useRef(null);
  const retryRef = useRef(0);
  const timerRef = useRef(null);

  useEffect(() => {
    let disposed = false;

    if (!token) {
      setConnected(false);
      return () => {
        disposed = true;
      };
    }

    function connect() {
      if (disposed) return;
      const socket = new WebSocket(resolveWsUrl());

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
          if (frame.type === "execution:pnl" && frame.data) {
            setPnl((prev) => ({
              last: frame.data,
              history: [frame.data, ...(prev?.history || [])].slice(0, 40),
            }));
          } else if (
            frame.type === "diagnostics:events" &&
            frame.data &&
            frame.data.match_id
          ) {
            setDiagnostics((prev) => [frame.data, ...prev].slice(0, 60));
          } else if (frame.type === "execution:commands" && frame.data) {
            setCommands((prev) => [frame.data, ...prev].slice(0, 60));
          }
          // matches:live is ignored here — the public hook tracks the grid.
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
  }, [token]);

  return { connected, lastEvent, pnl, diagnostics, commands };
}