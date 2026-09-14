"use strict";

/**
 * Relay admin-tier integration test (needs a reachable REDIS_URL):
 * spawns the real server on an ephemeral port with an ADMIN_SECRET, then
 * verifies:
 *   - /api/auth/token rejects wrong secrets, accepts the right one
 *   - /api/stats is 401 without a session token, 200 with one
 *   - publishing a fake PnL settlement is reflected in the outcomes ledger
 *     and channel staleness
 */
const { spawn } = require("child_process");
const http = require("http");
const redis = require("redis");

const REDIS_URL = process.env.REDIS_URL || "redis://localhost:6379/0";
const ADMIN_SECRET = "test-admin-secret";

function requestJson({ port, path, method = "GET", token = null, body = null }) {
  return new Promise((resolve, reject) => {
    const headers = {};
    if (token) headers["Authorization"] = `Bearer ${token}`;
    if (body) headers["Content-Type"] = "application/json";
    const req = http.request(
      { host: "127.0.0.1", port, path, method, headers, timeout: 3000 },
      (res) => {
        let data = "";
        res.on("data", (d) => (data += d));
        res.on("end", () => {
          let parsed = null;
          try {
            parsed = JSON.parse(data);
          } catch {
            parsed = data;
          }
          resolve({ status: res.statusCode, body: parsed });
        });
      }
    );
    req.on("timeout", () => req.destroy(new Error("request timeout")));
    req.on("error", reject);
    if (body) req.write(JSON.stringify(body));
    req.end();
  });
}

function waitFor(port, attempt = 0) {
  if (attempt > 60) throw new Error("relay did not come up");
  return requestJson({ port, path: "/healthz" })
    .then(() => true)
    .catch(() => new Promise((r) => setTimeout(r, 250)).then(() => waitFor(port, attempt + 1)));
}

async function main() {
  const child = spawn(process.execPath, ["server.js"], {
    cwd: __dirname,
    env: { ...process.env, REDIS_URL, PORT: "0", ADMIN_SECRET },
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

    const before = await requestJson({ port, path: "/api/stats" });
    if (before.status !== 401) throw new Error(`expected 401 without token, got ${before.status}`);

    const badSecret = await requestJson({
      port,
      path: "/api/auth/token",
      method: "POST",
      body: { secret: "wrong-secret" },
    });
    if (badSecret.status !== 401) throw new Error(`expected 401 for wrong secret, got ${badSecret.status}`);

    const issued = await requestJson({
      port,
      path: "/api/auth/token",
      method: "POST",
      body: { secret: ADMIN_SECRET },
    });
    if (issued.status !== 200 || !issued.body.token) throw new Error("token issue failed");
    const token = issued.body.token;

    const authed = await requestJson({ port, path: "/api/stats", token });
    if (authed.status !== 200) throw new Error(`expected 200 with token, got ${authed.status}`);

    pub = redis.createClient({ url: REDIS_URL });
    await pub.connect();

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
    const after = await requestJson({ port, path: "/api/stats", token });
    if (after.status !== 200) throw new Error(`stats status ${after.status}`);

    const baseWon = authed.body.outcomes.won || 0;
    const baseLost = authed.body.outcomes.lost || 0;
    const baseNet = authed.body.outcomes.net || 0;
    // The live stack may publish settlements concurrently; assert lower
    // bounds rather than exact deltas so this stays race-free.
    if (after.body.outcomes.won < baseWon + 1) throw new Error("won not counted");
    if (after.body.outcomes.lost < baseLost + 1) throw new Error("lost not counted");
    if (after.body.outcomes.net < baseNet + 62.5 - 1e-6) throw new Error("net not accumulated");

    const staleness = after.body.channels["execution:pnl"];
    if (staleness === undefined) throw new Error("execution:pnl channel missing from staleness");
    if (staleness.age_seconds === null || staleness.last_seen_ms === null)
      throw new Error("channel staleness not tracked");

    if (!Array.isArray(after.body.model.weight_names)) throw new Error("model block missing");

    console.log(
      `admin-tier api/stats: OK  outcomes won=${after.body.outcomes.won} lost=${after.body.outcomes.lost} net=${after.body.outcomes.net.toFixed(2)} channel_age=${staleness.age_seconds}s`
    );
  } finally {
    await pub?.quit();
    child.kill();
  }
}

main().catch((err) => {
  console.error("RELAY STATS TEST FAILED:", err.message);
  process.exit(1);
});