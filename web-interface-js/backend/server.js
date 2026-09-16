"use strict";

const http = require("http");
const crypto = require("crypto");
const { WebSocketServer } = require("ws");
const redis = require("redis");

const PORT = process.env.PORT || 8080;
const REDIS_URL = process.env.REDIS_URL;
// Public sockets only ever see the live match feed. Everything else — graded
// PnL settlements, diagnostics, and the HMAC-signed execution:commands bus —
// is admin-tier and requires a valid ADMIN_SECRET bearer token.
const ADMIN_SECRET = process.env.ADMIN_SECRET || "";
const ADMIN_TOKEN_TTL_SECONDS = 8 * 3600;
const PUBLIC_CHANNEL = process.env.WS_MATCHES_CHANNEL || "matches:live";
const WS_CHANNELS = [
  PUBLIC_CHANNEL,
  process.env.WS_DIAG_CHANNEL || "diagnostics:events",
  process.env.WS_PNL_CHANNEL || "execution:pnl",
  process.env.WS_COMMANDS_CHANNEL || "execution:commands",
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

// --- token primitives ---

function safeEqual(a, b) {
  const ba = Buffer.from(String(a));
  const bb = Buffer.from(String(b));
  if (ba.length !== bb.length) return false;
  return crypto.timingSafeEqual(ba, bb);
}

// Envelope mirrors the repo's signed-frame convention: base64url(payload)
// "." base64url(HMAC-SHA256(payload, ADMIN_SECRET)), unpadded both halves.
function issueAdminToken() {
  const now = Math.floor(Date.now() / 1000);
  const payload = Buffer.from(
    JSON.stringify({ sub: "admin", iat: now, exp: now + ADMIN_TOKEN_TTL_SECONDS })
  ).toString("base64url");
  const sig = crypto.createHmac("sha256", ADMIN_SECRET).update(payload).digest("base64url");
  return { token: `${payload}.${sig}`, expires_at: now + ADMIN_TOKEN_TTL_SECONDS };
}

function verifyAdminToken(token) {
  if (!token || !ADMIN_SECRET) return false;
  const parts = String(token).split(".");
  if (parts.length !== 2) return false;
  const [payload, sig] = parts;
  const expected = crypto.createHmac("sha256", ADMIN_SECRET).update(payload).digest("base64url");
  if (!safeEqual(expected, sig)) return false;
  try {
    const body = JSON.parse(Buffer.from(payload, "base64url").toString("utf8"));
    return body.sub === "admin" && Number.isFinite(body.exp) && body.exp > Math.floor(Date.now() / 1000);
  } catch {
    return false;
  }
}

function bearerFrom(req) {
  const auth = req.headers["authorization"];
  if (auth && /^Bearer\s+/i.test(auth)) return auth.slice(7).trim();
  try {
    return new URL(req.url, "http://localhost").searchParams.get("token") || "";
  } catch {
    return "";
  }
}

// --- http plumbing ---

function cors(res) {
  res.setHeader("Access-Control-Allow-Origin", "*");
  res.setHeader("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
  res.setHeader("Access-Control-Allow-Headers", "Content-Type, Authorization");
  res.setHeader("Access-Control-Max-Age", "600");
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
  if (req.url === "/api/auth/token" && req.method === "POST") {
    handleTokenIssue(req, res);
    return;
  }
  if (req.url === "/api/stats" && req.method === "GET") {
    if (!client.isOpen) {
      sendJson(res, 503, { status: "starting", message: "redis connecting" });
      return;
    }
    if (!verifyAdminToken(bearerFrom(req))) {
      sendJson(res, 401, { status: "error", message: "admin bearer token required" });
      return;
    }
    handleStats(res);
    return;
  }
  res.writeHead(404);
  res.end("not found");
});

function handleTokenIssue(req, res) {
  if (!ADMIN_SECRET) {
    sendJson(res, 503, { status: "error", message: "admin tier not configured (ADMIN_SECRET unset)" });
    return;
  }
  let body = "";
  req.on("data", (d) => (body += d));
  req.on("end", () => {
    let secret = "";
    try {
      secret = String(JSON.parse(body).secret || "");
    } catch {
      secret = "";
    }
    if (!secret || !safeEqual(secret, ADMIN_SECRET)) {
      sendJson(res, 401, { status: "error", message: "invalid admin secret" });
      return;
    }
    const issued = issueAdminToken();
    sendJson(res, 200, {
      status: "ok",
      token: issued.token,
      expires_at: issued.expires_at,
      expires_in: ADMIN_TOKEN_TTL_SECONDS,
    });
  });
}

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
      channels[ch] = lastTs
        ? { last_seen_ms: lastTs, age_seconds: Math.max(0, Math.floor((now - lastTs) / 1000)) }
        : { last_seen_ms: null, age_seconds: null };
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
        weight_names: (model.features ? JSON.parse(model.features) : []).map((name, i) => ({
          name,
          weight: weights[i] ?? null,
        })),
      },
      pnl: {
        net: pnl.net !== undefined ? Number(pnl.net) : 0,
        won: Number(pnl.won || 0),
        lost: Number(pnl.lost || 0),
        voids: Number(pnl.voids || 0),
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

// --- websocket role tiering ---

const wss = new WebSocketServer({ noServer: true });

server.on("upgrade", (req, socket, head) => {
  const pathname = (req.url || "").split("?")[0];
  if (pathname !== "/ws") {
    socket.destroy();
    return;
  }
  let token = "";
  try {
    token = new URL(req.url, "http://localhost").searchParams.get("token") || "";
  } catch {
    token = "";
  }
  const role = token && verifyAdminToken(token) ? "admin" : "public";
  wss.handleUpgrade(req, socket, head, (ws) => {
    ws.role = role;
    wss.emit("connection", ws, req);
  });
});

wss.on("connection", (socket, req) => {
  console.log(`client connected (role=${socket.role})`);
  socket.on("close", () => console.log(`client disconnected (role=${socket.role})`));
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

      // Public sockets see ONLY the live match feed; admin sockets get the
      // full bus (pnl, diagnostics, signed commands).
      for (const socket of wss.clients) {
        if (socket.readyState !== socket.OPEN) continue;
        if (socket.role !== "admin" && ch !== PUBLIC_CHANNEL) continue;
        socket.send(framed);
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