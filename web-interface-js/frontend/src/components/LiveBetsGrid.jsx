"use client";

/**
 * Derive the bookmaker favourite from the first quoted market (lowest odds
 * wins). Frames carry no top-level `winner` field, so it is computed here.
 */
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

/**
 * Renders matches currently inside the 10-15 minute "Study Room" window with
 * real-time odds tracking and active execution notifications.
 */
export default function LiveBetsGrid({ matches, lastEvent }) {
  return (
    <section
      style={{
        border: "1px solid #21262d",
        borderRadius: "8px",
        padding: "16px",
        marginBottom: "20px",
        backgroundColor: "#0d141f",
      }}
    >
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          marginBottom: "12px",
        }}
      >
        <h2 style={{ margin: 0, fontSize: "16px" }}>STUDY ROOM — LIVE BETS</h2>
        <span style={{ fontSize: "12px", color: "#8b949e" }}>
          {matches.length} in-window match(es)
        </span>
      </div>

      {matches.length === 0 ? (
        <p style={{ color: "#8b949e", fontSize: "13px" }}>
          Awaiting study-phase match data…
        </p>
      ) : (
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))",
            gap: "12px",
          }}
        >
          {matches.map((match) => (
            <article
              key={match.match_id}
              style={{
                border: "1px solid #21262d",
                borderRadius: "6px",
                padding: "12px",
              }}
            >
              <div style={{ display: "flex", justifyContent: "space-between" }}>
                <strong>{match.home_team} vs {match.away_team}</strong>
                <span style={{ color: "#58a6ff", fontSize: "12px" }}>
                  {match.clock}
                </span>
              </div>
              <p style={{ margin: "6px 0", color: "#8b949e", fontSize: "12px" }}>
                {match.sport} · {match.league}
              </p>
              <div style={{ display: "flex", gap: "8px", flexWrap: "wrap", marginTop: "8px" }}>
                {Object.entries(match.markets || {}).map(([id, market]) => (
                  <span
                    key={id}
                    style={{
                      fontSize: "12px",
                      padding: "4px 8px",
                      border: "1px solid #30363d",
                      borderRadius: "4px",
                      backgroundColor: "#161b22",
                    }}
                  >
                    {id}: H {market.odds?.home ?? "-"} · D{" "}
                    {market.odds?.draw ?? "-"} · A {market.odds?.away ?? "-"}
                  </span>
                ))}
              </div>
              <p style={{ margin: "8px 0 0", fontSize: "12px", color: "#3fb950" }}>
                {(() => {
                  const fav = favouriteOf(match);
                  return fav ? `favouring ${fav}` : "";
                })()}
              </p>
            </article>
          ))}
        </div>
      )}

      {lastEvent && (
        <footer style={{ marginTop: "12px", fontSize: "11px", color: "#484f58" }}>
          last frame: {lastEvent.type} @ {new Date(lastEvent.ts).toLocaleTimeString()}
        </footer>
      )}
    </section>
  );
}