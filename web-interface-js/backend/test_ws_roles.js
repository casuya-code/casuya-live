"use strict";

/**
 * Relay WebSocket role-tiering test (needs a reachable REDIS_URL):
 * spawns the real server on an ephemeral port with an ADMIN_SECRET, then
 * verifies the public/admin split at handshake time:
 *   - public socket (no token) receives matches:live but NOT execution:pnl
 *     or execution:commands
 *   - admin socket (session token) receives matches:live AND execution:pnl
 *     AND execution:commands
 */
const { spawn } = require("child_process");
const http = require("http");
const { WebSocket } = require("ws");
const redis = require("redis");

const REDIS_URL = process.env.REDIS_URL || "redis://localhost:6379/0";
const ADMIN_SECRET = "test-admin-secret";

function postJson(port, path, body) {
  return new Promise((resolve, reject) => {
    const req = http.request(
      { host: "127.0.0.1", port, path, method: "POST", headers: { "Content-Type": "application/json" }, timeout: 3000 },
      (res) => {
        let data = "";
        res.on("data", (d) => (data += d));
        res.on("end", () => resolve({ status: res.statusCode, body: JSON.parse(data) }));
      }
    );
    req.on("error", reject);
    req.write(JSON.stringify(body));
    req.end();
  });
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}

async function waitFor(port) {
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      const status = await new Promise((resolve, reject) => {
        const req = http.get(
          { host: "127.0.0.1", port, path: "/healthz", timeout: 2000 },
          (res) => {
            res.resume();
            resolve(res.statusCode);
          }
        );
        req.on("timeout", () => req.destroy());
        req.on("error", () => reject(new Error("down")));
      });
      if (status === 200) return true;
    } catch {
      // relay not up yet
    }
    await sleep(250);
  }
  throw new Error("relay did not come up");
}

function connect(port, token) {
  const url = token ? `ws://127.0.0.1:${port}/ws?token=${encodeURIComponent(token)}` : `ws://127.0.0.1:${port}/ws`;
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(url);
    const frames = [];
    const timer = setTimeout(() => reject(new Error("ws connect timeout")), 5000);
    ws.on("message", (data) => frames.push(JSON.parse(String(data))));
    ws.once("open", () => {
      clearTimeout(timer);
      resolve({ ws, frames, types: () => frames.map((f) => f.type) });
    });
    ws.once("error", (err) => {
      clearTimeout(timer);
      reject(err);
    });
  });
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
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
  let publicWs = null;
  let adminWs = null;
  try {
    await waitFor(port);

    const issued = await postJson(port, "/api/auth/token", { secret: ADMIN_SECRET });
    if (issued.status !== 200) throw new Error("token issue failed");

    publicWs = await connect(port, null);
    adminWs = await connect(port, issued.body.token);

    pub = redis.createClient({ url: REDIS_URL });
    await pub.connect();

    // Give both sockets a chance to be subscribed inside the relay.
    await sleep(400);

    await pub.publish(
      "matches:live",
      JSON.stringify({ type: "frame", match_id: "role-test-match", clock: "LIVE", markets: {} })
    );
    await pub.publish(
      "execution:pnl",
      JSON.stringify({
        type: "pnl",
        cycle: 1,
        match_id: "role-test-match",
        final_score: { home: 0, away: 0 },
        settled: [{ order_id: "R1", result: "won", pnl: 42 }],
      })
    );
    await pub.publish(
      "execution:commands",
      JSON.stringify({ type: "command", match_id: "role-test-match", market_id: "CS_1H", side: "away" })
    );

    await sleep(700);

    const publicTypes = publicWs.types();
    const adminTypes = adminWs.types();

    const log = (s) => console.log(`ws roles: ${s}`);

    if (!publicTypes.includes("matches:live")) throw new Error("public socket missed matches:live");
    if (publicTypes.includes("execution:pnl")) throw new Error("execution:pnl LEAKED to public socket");
    if (publicTypes.includes("execution:commands")) throw new Error("execution:commands LEAKED to public socket");

    if (!adminTypes.includes("matches:live")) throw new Error("admin socket missed matches:live");
    if (!adminTypes.includes("execution:pnl")) throw new Error("admin socket missed execution:pnl");
    if (!adminTypes.includes("execution:commands")) throw new Error("admin socket missed execution:commands");

    if (adminTypes.filter((t) => t === "execution:pnl").length < 1)
      throw new Error("admin socket only got partial pnl frames");

    log(`public=${publicTypes.join(",")}  admin=${adminTypes.join(",")}`);
    log(`OK  public sees matches only; admin sees full bus`);
  } finally {
    await publicWs?.ws.close();
    await adminWs?.ws.close();
    await pub?.quit();
    child.kill();
  }
}

main().catch((err) => {
  console.error("RELAY WS ROLES TEST FAILED:", err.message);
  process.exit(1);
});