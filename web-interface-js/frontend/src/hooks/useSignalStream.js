"use client";

import { useEffect, useRef, useState } from "react";
import { engineWsUrl } from "../lib/engine";

/**
 * Live signal stream hook. Connects straight to the Go executor's
 * /api/v1/stream/signals WebSocket and collects each "signal" frame into a
 * prepended, de-duplicated queue. The most recent order id flashes in the UI.
 */
export default function useSignalStream() {
  const [connected, setConnected] = useState(false);
  const [signals, setSignals] = useState([]);
  const [flashId, setFlashId] = useState(null);

  const socketRef = useRef(null);
  const retryRef = useRef(0);
  const timerRef = useRef(null);

  /**
   * Seeds the queue from the REST active-orders snapshot when the WebSocket
   * is not connected, so the signal desk still shows unsettled orders while
   * the stream is down.
   */
  function seedSignals(list) {
    if (!Array.isArray(list) || list.length === 0) return;
    setSignals((prev) => {
      if (prev.length > 0) {
        const known = new Set(prev.map((s) => s.order_id));
        return [...prev, ...list.filter((s) => !known.has(s.order_id))].slice(0, 60);
      }
      return list.slice(0, 60);
    });
    setFlashId(list[0]?.order_id || null);
    setTimeout(() => setFlashId(null), 1500);
  }

  useEffect(() => {
    let disposed = false;

    function connect() {
      if (disposed) return;
      const socket = new WebSocket(engineWsUrl());

      socket.onopen = () => {
        if (disposed) return;
        setConnected(true);
        retryRef.current = 0;
      };

      socket.onmessage = (event) => {
        if (disposed) return;
        try {
          const frame = JSON.parse(event.data);
          if (frame.type !== "signal" || !frame.order_id) return;
          setSignals((prev) =>
            [frame, ...prev.filter((s) => s.order_id !== frame.order_id)].slice(
              0,
              60
            )
          );
          setFlashId(frame.order_id);
          setTimeout(() => {
            setFlashId((cur) => (cur === frame.order_id ? null : cur));
          }, 1500);
        } catch {
          // ignore malformed frames
        }
      };

      socket.onerror = () => socket.close();

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

  return { connected, signals, flashId, seedSignals };
}