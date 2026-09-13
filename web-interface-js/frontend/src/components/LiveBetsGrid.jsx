"use client";

import { useEffect, useRef, useState } from "react";

const MIN_TRIGGER_ODDS = 5.0;

const MARKET_LABELS = {
  "1H_1X2": "1H · 1X2",
  "2H_1X2": "2H · 1X2",
  "CS_1H": "CS · 1H",
  "CS_2H": "CS · 2H",
};

function prettyMarket(id) {
  return MARKET_LABELS[id] || id.replace("_", " · ");
}

function favouriteOf(match) {
  const markets = Object.values(match.markets || {});
  if (markets.length === 0) return "";
  const odds = markets[0].odds || {};
  let best = "";
  let bestPrice = Infinity;
  for (const side of ["home", "draw", "away"]) {
    const price = Number(odds[side]);
    if (Number.isFinite(price) && price > 0 && price < bestPrice) {
      bestPrice = price;
      best = side;
    }
  }
  return best;
}

function hasValue(match) {
  return Object.values(match.markets || {}).some((m) =>
    Object.values(m.odds || {}).some(
      (p) => Number(p) >= MIN_TRIGGER_ODDS && Number.isFinite(Number(p))
    )
  );
}

function presence(value) {
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

/**
 * Odds cell with a reactive flash on price movement: up = green, down = red.
 */
function PriceChip({ side, price, leader }) {
  const value = presence(price);
  const prev = useRef(value);
  const [flash, setFlash] = useState(null);

  useEffect(() => {
    if (prev.current !== null && value !== null && prev.current !== value) {
      setFlash(value > prev.current ? "price--up" : "price--down");
      const t = setTimeout(() => setFlash(null), 700);
      return () => clearTimeout(t);
    }
    prev.current = value;
  }, [value]);

  if (value === null) {
    return (
      <div className="price">
        <span className="side">{side}</span>
        <span className="num">
          {Number.isFinite(presence(price)) ? price : "—"}
        </span>
      </div>
    );
  }

  return (
    <div className={`price ${flash || ""} ${leader ? "price--leader" : ""}`}>
      <span className="side">{side}</span>
      <span className="num">{price.toFixed(2)}</span>
    </div>
  );
}

function Momentum({ match }) {
  const shots = presence(match.shots);
  const sot = presence(match.shots_on_target);
  const dangerous = presence(match.dangerous_attacks);
  const poss = presence(match.possession_home);

  if (shots === null && dangerous === null && poss === null) return null;

  return (
    <div className="momentum">
      {poss !== null && (
        <>
          <div className="m-row">
            <span>poss {Math.round(poss * 100)}%</span>
            <span>shots {shots ?? "—"} · sot {sot ?? "—"}</span>
          </div>
          <div className="m-bar">
            <span className="m-home" style={{ width: `${Math.round(poss * 100)}%` }} />
            <span className="m-away" style={{ flex: 1 }} />
          </div>
        </>
      )}
      {dangerous !== null && (
        <div className="m-row" style={{ marginTop: 6 }}>
          <span>danger</span>
          <span style={{ color: "#38bdf8" }}>{dangerous}</span>
        </div>
      )}
    </div>
  );
}

function MatchCard({ match }) {
  const fav = favouriteOf(match);
  const scoreHome = presence(match.score?.home);
  const scoreAway = presence(match.score?.away);
  const kickoff = presence(match.kickoff);
  const kickoffAt = kickoff
    ? new Date(kickoff * 1000).toLocaleTimeString([], {
        hour: "2-digit",
        minute: "2-digit",
      })
    : null;

  return (
    <article className="card card--lift" style={{ padding: "16px" }}>
      <header
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          gap: "8px",
          marginBottom: "12px",
          flexWrap: "wrap",
        }}
      >
        <span className="eyebrow">{match.league || "Trading"}</span>
        <span className="chip">
          <span className="live-dot" style={{ width: 6, height: 6 }} />
          {match.clock || "KO"}
        </span>
      </header>

      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "flex-start",
          gap: "10px",
          marginBottom: "4px",
        }}
      >
        <div className="team">
          <div className="team-name" title={match.home_team}>
            {match.home_team}
          </div>
          <div
            className="team-name"
            title={match.away_team}
            style={{ opacity: 0.85 }}
          >
            {match.away_team}
          </div>
        </div>
        <div className="scoreboard">
          {scoreHome !== null && scoreAway !== null ? (
            <>
              <span className="score-num">{scoreHome}</span>
              <span style={{ color: "var(--text-3)", fontSize: "18px" }}>–</span>
              <span className="score-num">{scoreAway}</span>
            </>
          ) : (
            <span className="mono" style={{ color: "var(--text-3)", fontSize: 12 }}>
              {match.sport || "pre-match"}
            </span>
          )}
        </div>
      </div>

      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          gap: "8px",
          minHeight: "22px",
          marginBottom: "12px",
        }}
      >
        {hasValue(match) ? (
          <span className="badge badge--value">
            <span className="live-dot" style={{ width: 5, height: 5 }} />
            VALUE
          </span>
        ) : (
          <span className="eyebrow">{match.sport || ""}</span>
        )}
        {kickoffAt && (
          <span className="mono" style={{ fontSize: 11, color: "var(--text-3)" }}>
            kick {kickoffAt}
          </span>
        )}
      </div>

      <div
        style={{
          display: "grid",
          gap: "10px",
        }}
      >
        {Object.entries(match.markets || {}).map(([id, market]) => {
          const odds = market.odds || {};
          return (
            <div key={id}>
              <div
                className="eyebrow"
                style={{ marginBottom: "6px", display: "flex", gap: "6px", alignItems: "center" }}
              >
                {prettyMarket(id)}
                {fav && (
                  <span style={{ color: "var(--accent-2)", fontWeight: 700 }}>
                    ({fav})
                  </span>
                )}
              </div>
              <div className="odds-grid">
                <PriceChip side="home" price={odds.home} leader={fav === "home"} />
                <PriceChip side="draw" price={odds.draw} leader={fav === "draw"} />
                <PriceChip side="away" price={odds.away} leader={fav === "away"} />
              </div>
            </div>
          );
        })}
      </div>

      <Momentum match={match} />
    </article>
  );
}

export default function LiveBetsGrid({ matches, lastEvent }) {
  return (
    <section className="card" style={{ padding: "20px" }}>
      <div className="section-head">
        <div className="section-title">
          <span className="badge badge--live">live</span>
          <h2>STUDY ROOM — LIVE BETS</h2>
          <span className="section-rule" />
        </div>
        <span className="chip">{matches.length} in-window</span>
      </div>

      {matches.length === 0 ? (
        <div className="empty">Awaiting study-phase match data…</div>
      ) : (
        <div className="card-grid">
          {matches.map((match) => (
            <MatchCard key={match.match_id} match={match} />
          ))}
        </div>
      )}

      {lastEvent && (
        <footer className="page-foot" style={{ marginTop: "18px" }}>
          <span>last frame</span>
          <span className="mono">
            {lastEvent.type} @ {new Date(lastEvent.ts).toLocaleTimeString()}
          </span>
        </footer>
      )}
    </section>
  );
}