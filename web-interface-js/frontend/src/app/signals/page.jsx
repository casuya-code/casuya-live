"use client";

import { useEffect, useState } from "react";
import PnLTracker from "../../components/PnLTracker";
import SignalGrid from "../../components/SignalGrid";
import useSignalStream from "../../hooks/useSignalStream";
import {
  fetchActiveSignals,
  fetchLedger,
  fetchPlacements,
  fetchPnl,
  recordPlacement,
} from "../../lib/engine";

export default function SignalsPage() {
  const { connected, signals, flashId, seedSignals } = useSignalStream();
  const [fnl, setFnl] = useState(null);
  const [ledger, setLedger] = useState([]);
  const [placements, setPlacements] = useState({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  useEffect(() => {
    let disposed = false;

    async function load() {
      try {
        const [pnl, active, led, placed] = await Promise.all([
          fetchPnl(),
          fetchActiveSignals(),
          fetchLedger(60),
          fetchPlacements(60),
        ]);
        if (!disposed) {
          setFnl(pnl);
          setLedger(led.entries || []);
          setError(null);
          const pm = {};
          (placed.entries || []).forEach((e) => {
            pm[e.order_id] = e;
          });
          setPlacements(pm);
          if (!connected && active?.signals?.length) {
            seedSignals(active.signals);
          }
        }
      } catch (err) {
        if (!disposed) setError(err.message);
      } finally {
        if (!disposed) setLoading(false);
      }
    }

    load();
    const timer = setInterval(load, 15000);
    return () => {
      disposed = true;
      clearInterval(timer);
    };
  }, [connected]);

  const pending = signals.filter(
    (s) => !ledger.some((e) => e.order_id === s.order_id)
  );

  async function handlePlace(signal) {
    const payload = {
      order_id: signal.order_id,
      match_id: signal.match_id,
      market: signal.market,
      side: signal.side,
      odds: Number(signal.odds) || 0,
      stake: Number(signal.stake) || 0,
      book: "helabet",
      status: "placed",
    };
    try {
      await recordPlacement(payload);
      setPlacements((prev) => ({
        ...prev,
        [signal.order_id]: { ...payload, placed_at: Date.now() },
      }));
    } catch (err) {
      setError(err.message);
    }
  }

  return (
    <>
      <nav className="nav">
        <div className="nav-inner">
          <div className="brand">
            <span className="brand-mark">C</span>
            <div>
              <h1>CASUYA-LIVE</h1>
              <p className="tagline">Algorithmic signal &amp; analytics desk</p>
            </div>
          </div>
          <div className="nav-meta">
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
            <a
              href="/admin"
              className="pill mono"
              style={{
                color: "var(--text-3)",
                textDecoration: "none",
                border: "1px solid var(--hairline)",
                padding: "4px 10px",
                borderRadius: "8px",
              }}
            >
              OPERATOR
            </a>
            <span className={`pill ${connected ? "mono" : "mono"}`}>
              <span
                className={`live-dot ${connected ? "" : "live-dot--off"}`}
              />
              {connected ? "ENGINE ONLINE" : "ENGINE OFFLINE"}
            </span>
          </div>
        </div>
      </nav>

      <main className="container">
        <section className="card card--lift section-head" style={{ padding: "16px 18px" }}>
          <div className="section-title">
            <span className="eyebrow">signal desk</span>
            <h2 style={{ fontSize: "20px" }}>Algorithmic Signals &amp; Paper P&amp;L</h2>
          </div>
          <div className="nav-meta">
            <span className="chip">{pending.length} pending signals</span>
            {error && (
              <span className="chip" style={{ color: "var(--danger)" }}>
                {error}
              </span>
            )}
          </div>
        </section>

        <SignalGrid
          signals={signals}
          flashId={flashId}
          connected={connected}
          placements={placements}
          onPlace={handlePlace}
        />

        <div style={{ marginTop: "18px" }}>
          <PnLTracker pnl={fnl} ledger={ledger} loading={loading} />
        </div>

        <footer className="page-foot">
          <span>casuya-live · operator-assisted live desk — copy, place on helabet, mark placed</span>
          <span>background engine · paper grading runs in parallel</span>
        </footer>
      </main>
    </>
  );
}