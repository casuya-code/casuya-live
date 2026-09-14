"use strict";

const http = require("http");
const { WebSocketServer } = require("ws");
const redis = require("redis");

const PORT = process.env.PORT || 8080;
const REDIS_URL = process.env.REDIS_URL;
// NOTE: execution:commands carries HMAC-signed order payloads (stakes,
// sides, quotes). It is internal-only and must never be relayed to public
// dashboard clients. Override WS_EXTRA_CHANNELS explicitly if a private
// operator view ever needs it.
// execution:pnl is a derived, operator-facing aggregate (graded results and
// session totals) and is safe to display on the dashboard.
const WS_CHANNELS = [
  process.env.WS_MATCHES_CHANNEL || "matches:live",
  process.env.WS_DIAG_CHANNEL || "diagnostics:events",
  process.env.WS_PNL_CHANNEL || "execution:pnl",
  ...(process.env.WS_EXTRA_CHANNELS
    ? process.env.WS_EXTRA_CHANNELS.split(",").map((c) => c.trim()).filter(Boolean)
    : []),
];

if (!REDIS_URL) {
  console.error("REDIS_URL is required");
  process.exit(1);
}

const OUTCOMES_HASH = "stats:outcomes";
const STALENESS_HASH = "relay:staleness";

function cors(res) {
  res.setHeader("Access-Control-Allow-Origin", "*");
  res.setHeader("Access-Control-Allow-Methods", "GET, OPTIONS");
  res.setHeader("Access-Control-Allow-Headers", "Content-Type");
}

function sendJson(res, code, body) {
  cors(res);
  res.writeHead(code, { "Content-Type": "application/json" });
  res.end(JSON.stringify(body));
}

const server = http.createServer((req, res) => {
  if (req.url === "/healthz") {
    sendJson(res, 200, { status: "ok", service: "casuya-live-web-relay" });
    return;
  }
  if (req.method === "OPTIONS") {
    cors(res);
    res.writeHead(204);
    res.end();
    return;
  }
  if (req.url === "/api/stats" && req.method === "GET") {
    if (!client.isOpen) {
      sendJson(res, 503, { status: "starting", message: "redis connecting" });
      return;
    }
    handleStats(res);
    return;
  }
  res.writeHead(404);
  res.end("not found");
});

async function handleStats(res) {
  try {
    const model = await client.hGetAll("calibration:model");
    const heartbeat = await client.get("calibration:heartbeat");
    const pnl = await client.hGetAll("pnl:session");
    const outcomes = await client.hGetAll(OUTCOMES_HASH);
    const staleness = await client.hGetAll(STALENESS_HASH);

    const now = Date.now();
    const ageOf = (iso) => {
      if (!iso) return null;
      const t = Date.parse(iso);
      return Number.isNaN(t) ? null : Math.max(0, Math.floor((now - t) / 1000));
    };

    const channels = {};
    for (const ch of WS_CHANNELS) {
      const lastTs = Number(staleness[ch]) || 0;
      channels[ch] = lastTs ? { last_seen_ms: lastTs, age_seconds: Math.max(0, Math.floor((now - lastTs) / 1000)) } : { last_seen_ms: null, age_seconds: null };
    }

    const weights = model.weights
      ? model.weights.replace(/^\[|\]$/g, "").split(",").filter(Boolean).map(Number)
      : [];

    sendJson(res, 200, {
      service: "casuya-live-web-relay",
      generated_at: new Date().toISOString(),
      model: {
        status: model.status || null,
        generated_at: model.generated_at || null,
        heartbeat: heartbeat || null,
        status_age_seconds: ageOf(model.generated_at),
        rows: model.rows ? Number(model.rows) : null,
        train_rows: model.train_rows ? Number(model.train_rows) : null,
        val_rows: model.val_rows ? Number(model.val_rows) : null,
        features: model.features ? JSON.parse(model.features) : [],
        bias: model.bias !== undefined ? Number(model.bias) : null,
        l2: model.l2 !== undefined ? Number(model.l2) : null,
        train_accuracy: model.train_accuracy !== undefined ? Number(model.train_accuracy) : null,
        train_brier: model.train_brier !== undefined ? Number(model.train_brier) : null,
        val_accuracy: model.val_accuracy !== undefined ? Number(model.val_accuracy) : null,
        val_brier: model.val_brier !== undefined ? Number(model.val_brier) : null,
        val_auc: model.val_auc !== undefined ? Number(model.val_auc) : null,
        baseline_logloss: model.baseline_logloss !== undefined ? Number(model.baseline_logloss) : null,
        model_logloss: model.model_logloss !== undefined ? Number(model.model_logloss) : null,
        weights,
        weight_names: (model.features ? JSON.parse(model.features) : []).map((name, i) => ({ name, weight: weights[i] ?? null })),
      },
      pnl: {
        net: pnl.net !== undefined ? Number(pnl.net) : 0,
        won: Number(pnl.won || 0),
        lost: Number(pnl.lost || 0),
        updated_cycle: pnl.updated_cycle ? Number(pnl.updated_cycle) : null,
      },
      outcomes: {
        net: outcomes.net !== undefined ? Number(outcomes.net) : 0,
        won: Number(outcomes.won || 0),
        lost: Number(outcomes.lost || 0),
      },
      channels,
    });
  } catch (err) {
    console.error("stats error:", err);
    sendJson(res, 500, { status: "error", message: err.message });
  }
}

const wss = new WebSocketServer({ server, path: "/ws" });

wss.on("connection", (socket) => {
  console.log("client connected");
  socket.on("close", () => console.log("client disconnected"));
  socket.on("error", (err) => console.error("socket error:", err.message));
});

// Two Redis connections are required: the command client (hGetAll/hIncrBy/
// hSet) and a dedicated subscriber connection. node-redis v4 forces a socket
// used for subscribe() into subscriber mode, where regular commands error.
const client = redis.createClient({ url: REDIS_URL });
client.on("error", (err) => console.error("redis error:", err.message));
const sub = redis.createClient({ url: REDIS_URL });
sub.on("error", (err) => console.error("redis sub error:", err.message));

async function main() {
  // Serve healthz before the Redis handshake resolves so the relay is
  // observable even while pub/sub reconnects.
  server.listen(PORT, () => {
    console.log(`web relay listening on :${server.address().port}`);
  });

  await Promise.all([client.connect(), sub.connect()]);
  for (const channel of WS_CHANNELS) {
    await sub.subscribe(channel, (message, ch) => {
      let payload;
      try {
        payload = JSON.parse(message);
      } catch {
        payload = { raw: message };
      }
      const framed = JSON.stringify({
        type: ch,
        data: payload,
        ts: Date.now(),
      });
      for (const socket of wss.clients) {
        if (socket.readyState === socket.OPEN) {
          socket.send(framed);
        }
      }

      // Operator stats ledger: count settled outcomes as they stream in,
      // and remember per-channel freshness for the staleness monitor.
      if (ch === process.env.WS_PNL_CHANNEL || ch === "execution:pnl") {
        const settled = Array.isArray(payload.settled) ? payload.settled : [];
        if (settled.length) {
          let net = 0, won = 0, lost = 0;
          for (const order of settled) {
            net += Number(order.pnl || 0);
            if (order.result === "won") won += 1;
            else if (order.result === "lost") lost += 1;
          }
          client.hIncrByFloat(OUTCOMES_HASH, "net", net).catch(() => {});
          if (won) client.hIncrBy(OUTCOMES_HASH, "won", won).catch(() => {});
          if (lost) client.hIncrBy(OUTCOMES_HASH, "lost", lost).catch(() => {});
        }
      }
      client.hSet(STALENESS_HASH, ch, Date.now()).catch(() => {});
    });
    console.log(`relaying redis channel ${channel}`);
  }
}

process.on("SIGTERM", async () => {
  console.log("shutting down");
  wss.close();
  await Promise.allSettled([client.quit(), sub.quit()]);
  process.exit(0);
});

main().catch((err) => {
  console.error("startup failed:", err);
  process.exit(1);
});