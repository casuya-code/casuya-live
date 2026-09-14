"use strict";

/**
 * Relay /api/stats integration test (needs a reachable REDIS_URL):
 * spawns the real server on an ephemeral port, publishes a fake PnL
 * settlement, then asserts the outcomes ledger + channel staleness are
 * reflected in the stats JSON.
 */
const { spawn } = require("child_process");
const http = require("http");
const redis = require("redis");

const REDIS_URL = process.env.REDIS_URL || "redis://localhost:6379/0";

function getJson(port, path) {
  return new Promise((resolve, reject) => {
    http
      .get({ host: "127.0.0.1", port, path, timeout: 3000 }, (res) => {
        let body = "";
        res.on("data", (d) => (body += d));
        res.on("end", () => resolve({ status: res.statusCode, body: JSON.parse(body) }));
      })
      .on("error", reject);
  });
}

function waitFor(port, attempt = 0) {
  if (attempt > 60) throw new Error("relay did not come up");
  return getJson(port, "/healthz")
    .then(() => true)
    .catch(() => new Promise((r) => setTimeout(r, 250)).then(() => waitFor(port, attempt + 1)));
}

async function main() {
  const child = spawn(process.execPath, ["server.js"], {
    cwd: __dirname,
    env: { ...process.env, REDIS_URL, PORT: "0" },
    stdio: ["ignore", "pipe", "inherit"],
  });

  const port = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("no port line")), 8000);
    child.stdout.on("data", (buf) => {
      const m = String(buf).match(/listening on :(\d+)/);
      if (m) {
        clearTimeout(timer);
        resolve(Number(m[1]));
      }
    });
  });

  let pub = null;
  try {
    await waitFor(port);

    pub = redis.createClient({ url: REDIS_URL });
    await pub.connect();
    const before = await getJson(port, "/api/stats");

    await pub.publish(
      "execution:pnl",
      JSON.stringify({
        type: "pnl",
        cycle: 7,
        match_id: "stats-test",
        final_score: { home: 1, away: 2 },
        settled: [
          { order_id: "M1", side: "away", stake: 25, odds: 4.5, result: "won", pnl: 87.5 },
          { order_id: "M2", side: "home", stake: 25, odds: 2.0, result: "lost", pnl: -25 },
        ],
      })
    );

    await new Promise((r) => setTimeout(r, 500));
    const after = await getJson(port, "/api/stats");

    if (after.status !== 200) throw new Error(`stats status ${after.status}`);

    const baseWon = before.body.outcomes.won || 0;
    const baseLost = before.body.outcomes.lost || 0;
    const baseNet = before.body.outcomes.net || 0;
    // The live stack may publish settlements concurrently; assert lower
    // bounds rather than exact deltas so this stays race-free.
    if (after.body.outcomes.won < baseWon + 1) throw new Error("won not counted");
    if (after.body.outcomes.lost < baseLost + 1) throw new Error("lost not counted");
    if (after.body.outcomes.net < baseNet + 62.5 - 1e-6) throw new Error("net not accumulated");

    const staleness = after.body.channels["execution:pnl"];
    if (!staleness || staleness.age_seconds === null || staleness.last_seen_ms === null)
      throw new Error("channel staleness not tracked");

    if (!Array.isArray(after.body.model.weight_names)) throw new Error("model block missing");

    console.log(`api/stats: OK  outcomes won=${after.body.outcomes.won} lost=${after.body.outcomes.lost} net=${after.body.outcomes.net.toFixed(2)} channel_age=${staleness.age_seconds}s`);
  } finally {
    await pub?.quit();
    child.kill();
  }
}

main().catch((err) => {
  console.error("RELAY STATS TEST FAILED:", err.message);
  process.exit(1);
});