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
const WS_CHANNELS = [
  process.env.WS_MATCHES_CHANNEL || "matches:live",
  process.env.WS_DIAG_CHANNEL || "diagnostics:events",
  ...(process.env.WS_EXTRA_CHANNELS
    ? process.env.WS_EXTRA_CHANNELS.split(",").map((c) => c.trim()).filter(Boolean)
    : []),
];

if (!REDIS_URL) {
  console.error("REDIS_URL is required");
  process.exit(1);
}

const server = http.createServer((req, res) => {
  if (req.url === "/healthz") {
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ status: "ok", service: "casuya-live-web-relay" }));
    return;
  }
  res.writeHead(404);
  res.end("not found");
});

const wss = new WebSocketServer({ server, path: "/ws" });

wss.on("connection", (socket) => {
  console.log("client connected");
  socket.on("close", () => console.log("client disconnected"));
  socket.on("error", (err) => console.error("socket error:", err.message));
});

const client = redis.createClient({ url: REDIS_URL });
client.on("error", (err) => console.error("redis error:", err.message));

async function main() {
  // Serve healthz before the Redis handshake resolves so the relay is
  // observable even while pub/sub reconnects.
  server.listen(PORT, () => {
    console.log(`web relay listening on :${PORT}`);
  });

  await client.connect();
  for (const channel of WS_CHANNELS) {
    await client.subscribe(channel, (message, ch) => {
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
    });
    console.log(`relaying redis channel ${channel}`);
  }
}

process.on("SIGTERM", async () => {
  console.log("shutting down");
  wss.close();
  await client.quit();
  process.exit(0);
});

main().catch((err) => {
  console.error("startup failed:", err);
  process.exit(1);
});