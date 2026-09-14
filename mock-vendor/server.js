"use strict";

const path = require("path");
const crypto = require("crypto");

const { WebSocketServer } = require(
  path.join(__dirname, "..", "web-interface-js", "backend", "node_modules", "ws")
);

const AUTH_SECRET = process.env.INTERNAL_AUTH_SECRET || "local-dev-secret";
const VENDOR_PORT = parseInt(process.env.MOCK_VENDOR_PORT || "19999", 10);
const TRADE_PORT = parseInt(process.env.MOCK_TRADE_PORT || "19998", 10);

const REDIS_URL = process.env.REDIS_URL || "redis://localhost:6379/0";
const SETTLE_CHANNEL = process.env.SETTLE_CHANNEL || "bookmaker:settlements";

// --- settlement book -------------------------------------------------------
// Fills survive per-order connections (the executor dials once per order), so
// the book is server-global and keyed by order_id. Demos run a repeating
// ~40s cycle; the FULLTIME frame of every cycle publishes that cycle's fills
// for grading by the execution engine.
const CYCLE_MS = 40000;
const BOOT_EPOCH = Math.floor(Date.now() / 1000) - 31 * 60;
const cycleAt = (t) => 1 + Math.floor((Date.now() - BOOT_EPOCH * 1000) / CYCLE_MS);

const book = new Map();
let orderSeq = 0;

function redisPublisher() {
  let client = null;
  try {
    const redis = require(path.join(
      __dirname, "..", "web-interface-js", "backend", "node_modules", "redis"
    ));
    client = redis.createClient({ url: REDIS_URL });
    client.on("error", () => {});
    client.connect().then(
      () => console.log("[mock-vendor] redis connected (settlement bus)"),
      (err) => {
        console.log(`[mock-vendor] settlement bus offline (${err.message})`);
        client = null;
      }
    );
  } catch {
    console.log("[mock-vendor] redis module missing; settlement bus offline");
  }
  return client;
}

const redisClient = redisPublisher();

function publishSettlement(numberOfCycle, finalScore) {
  const orders = [];
  for (const [, o] of book) {
    if (o.cycle === numberOfCycle) orders.push(o);
  }
  const frame = {
    type: "settlement",
    cycle: numberOfCycle,
    match_id: "epl-2026-0042",
    final_score: finalScore,
    orders: orders.map((o) => ({
      order_id: o.order_id,
      market: o.market,
      side: o.side,
      odds: fmt(o.odds),
      stake: fmt(o.stake),
    })),
  };
  if (orders.length === 0) {
    console.log(`[mock-vendor] settle cycle ${numberOfCycle}: no orders`);
    return;
  }
const body = JSON.stringify(frame);
  if (redisClient && redisClient.isReady) {
    redisClient.publish(SETTLE_CHANNEL, body);
    console.log(`[mock-vendor] -> settlement cycle ${numberOfCycle} (${orders.length} orders)`);
  } else {
    console.log(`[mock-vendor] settlement (no redis): ${body}`);
  }
  for (const o of orders) book.delete(o.order_id);
}

function sign(body, secret) {
  return crypto.createHmac("sha256", secret).update(body).digest("base64url");
}

function b642(body) {
  return Buffer.from(body, "utf8").toString("base64");
}

function fmt(n) {
  return typeof n === "number" ? Number(n.toFixed(2)) : n;
}

function frame(match) {
  return {
    match_id: "epl-2026-0042",
    sport: "football",
    league: "england_premier_league",
    home_team: "Example FC",
    away_team: "Casual United",
    kickoff: match.kickoff,
    clock: match.clock,
    score: { home: match.score[0], away: match.score[1] },
    markets: {
      "1H_1X2": {
        market_id: "1H_1X2",
        odds: { home: fmt(match.o1h[0]), draw: fmt(match.o1h[1]), away: fmt(match.o1h[2]) },
        updated_at: match.kickoff + match.step * 60,
      },
      "2H_1X2": {
        market_id: "2H_1X2",
        odds: { home: fmt(match.o2h[0]), draw: fmt(match.o2h[1]), away: fmt(match.o2h[2]) },
        updated_at: match.kickoff + match.step * 60,
      },
    },
    shots: match.stats.shots,
    shots_on_target: match.stats.shots_on_target,
    dangerous_attacks: match.stats.dangerous_attacks,
    possession_home: fmt(match.stats.possession_home),
  };
}

// Low-tempo match. Casual United (away) dominate possession; their 2H price
// drifts to >= 5.0 by the 41st minute, which the analytics trigger requires.
// Home stays favourite so its legs are filtered below MIN_TRIGGER_ODDS.
const SCHEDULE = [
  { step: 0, clock: "31", score: [0, 0], o1h: [2.1, 3.4, 4.4], o2h: [2.2, 3.5, 4.6], stats: { shots: 3, shots_on_target: 0, dangerous_attacks: 9, possession_home: 0.36 } },
  { step: 1, clock: "34", score: [0, 0], o1h: [2.1, 3.4, 4.5], o2h: [2.2, 3.5, 4.7], stats: { shots: 4, shots_on_target: 1, dangerous_attacks: 10, possession_home: 0.35 } },
  { step: 2, clock: "37", score: [0, 1], o1h: [2.2, 3.5, 4.6], o2h: [2.35, 3.6, 4.9], stats: { shots: 5, shots_on_target: 1, dangerous_attacks: 11, possession_home: 0.34 } },
  { step: 3, clock: "41", score: [0, 1], o1h: [2.3, 3.5, 4.7], o2h: [2.45, 3.6, 5.1], stats: { shots: 5, shots_on_target: 1, dangerous_attacks: 11, possession_home: 0.34 } },
  { step: 4, clock: "45", score: [0, 1], o1h: [2.3, 3.5, 4.8], o2h: [2.5, 3.6, 5.2], stats: { shots: 6, shots_on_target: 1, dangerous_attacks: 12, possession_home: 0.33 } },
  { step: 5, clock: "HT", score: [0, 1], o1h: [2.3, 3.5, 4.8], o2h: [2.5, 3.6, 5.2], stats: { shots: 6, shots_on_target: 1, dangerous_attacks: 12, possession_home: 0.33 } },
  { step: 6, clock: "52", score: [0, 1], o1h: [2.4, 3.6, 4.8], o2h: [2.6, 3.7, 5.4], stats: { shots: 8, shots_on_target: 2, dangerous_attacks: 14, possession_home: 0.32 } },
  { step: 7, clock: "58", score: [0, 2], o1h: [2.5, 3.7, 4.9], o2h: [2.7, 3.8, 5.6], stats: { shots: 10, shots_on_target: 2, dangerous_attacks: 16, possession_home: 0.32 } },
  { step: 8, clock: "64", score: [0, 2], o1h: [2.6, 3.7, 4.9], o2h: [2.8, 3.8, 5.9], stats: { shots: 11, shots_on_target: 3, dangerous_attacks: 17, possession_home: 0.32 } },
  { step: 9, clock: "70", score: [0, 2], o1h: [2.7, 3.8, 5.0], o2h: [3.0, 3.9, 6.3], stats: { shots: 13, shots_on_target: 3, dangerous_attacks: 18, possession_home: 0.31 } },
  { step: 10, clock: "76", score: [0, 2], o1h: [2.8, 3.8, 5.1], o2h: [3.1, 4.0, 6.8], stats: { shots: 14, shots_on_target: 4, dangerous_attacks: 19, possession_home: 0.31 } },
  { step: 11, clock: "82", score: [1, 2], o1h: [2.9, 3.9, 5.2], o2h: [3.3, 4.1, 7.2], stats: { shots: 15, shots_on_target: 4, dangerous_attacks: 19, possession_home: 0.32 } },
  { step: 12, clock: "88", score: [1, 2], o1h: [3.1, 3.9, 5.3], o2h: [3.5, 4.1, 7.8], stats: { shots: 16, shots_on_target: 5, dangerous_attacks: 20, possession_home: 0.33 } },
  { step: 13, clock: "90", score: [1, 2], o1h: [3.2, 4.0, 5.4], o2h: [3.6, 4.2, 8.2], stats: { shots: 17, shots_on_target: 5, dangerous_attacks: 20, possession_home: 0.33 } },
  { step: 14, clock: "FULLTIME", score: [1, 2], o1h: [3.2, 4.0, 5.4], o2h: [3.6, 4.2, 8.2], stats: { shots: 17, shots_on_target: 5, dangerous_attacks: 20, possession_home: 0.33 } },
];

function vendorServer() {
  const wss = new WebSocketServer({ port: VENDOR_PORT, path: "/vendor" });
  console.log(`[mock-vendor] live feed   ws://localhost:${VENDOR_PORT}/vendor`);
  wss.on("connection", (socket) => {
    console.log("[mock-vendor] ingestor connected");
    const kickoff = Math.floor(Date.now() / 1000) - 31 * 60;
    let guard = 0;
    const timer = setInterval(() => {
      if (socket.readyState !== socket.OPEN || guard >= SCHEDULE.length) {
        clearInterval(timer);
        if (socket.readyState === socket.OPEN) socket.close();
        return;
      }
      const step = SCHEDULE[guard];
      const streamed = frame({ ...step, kickoff });
      socket.send(JSON.stringify(streamed));
      const minute = step.clock;
      const display = step.clock.length > 2 ? step.clock : `${step.clock}'`;
      console.log(`[mock-vendor] -> ${display} ${step.score[0]}-${step.score[1]}`);
      if (step.clock === "FULLTIME") {
        publishSettlement(cycleAt(Date.now()), { home: step.score[0], away: step.score[1] });
      }
      guard += 1;
    }, 2500);
    socket.on("close", () => clearInterval(timer));
  });
  return wss;
}

function verifySig(sig, body, secret) {
  try {
    const expected = crypto.createHmac("sha256", secret).update(body).digest();
    const supplied = Buffer.from(sig, "base64url");
    return supplied.length === expected.length && crypto.timingSafeEqual(supplied, expected);
  } catch {
    return false;
  }
}

function tradeServer() {
  const wss = new WebSocketServer({ port: TRADE_PORT, path: "/trade" });
  console.log(`[mock-vendor] bookmaker   ws://localhost:${TRADE_PORT}/trade`);
  wss.on("connection", (socket) => {
    console.log("[mock-vendor] executor connected");
    socket.on("message", (raw) => {
      let msg;
      try {
        msg = JSON.parse(raw.toString());
      } catch {
        socket.send(JSON.stringify({ status: "error", body: "bad frame" }));
        return;
      }
      if (msg.auth != null) {
        const body = Buffer.from(msg.auth, "base64");
        let parsed;
        try {
          parsed = JSON.parse(body.toString("utf8"));
        } catch {
          parsed = { ts: body.toString("utf8") };
        }
        if (!verifySig(msg.sig, body, AUTH_SECRET)) {
          socket.send(JSON.stringify({ status: "error", body: "bad auth sig" }));
          return;
        }
        const ackBody = Buffer.from(`ack ${parsed.ts || parsed.key || "ok"}`, "utf8");
        socket.send(JSON.stringify({
          status: "ok",
          body: ackBody.toString("base64"),
          sig: sign(ackBody, AUTH_SECRET),
        }));
        console.log("[mock-vendor] executor authenticated");
        return;
      }
      if (msg.body != null) {
        const body = Buffer.from(msg.body, "base64");
        const order = JSON.parse(body.toString("utf8"));
        if (!verifySig(msg.sig, body, AUTH_SECRET)) {
          socket.send(JSON.stringify({ status: "rejected", body: "bad order sig" }));
          return;
        }
        console.log(
          `[mock-vendor] ORDER ${order.type} ${order.market} ${order.side} @${order.odds} stake ${order.stake}`
        );
        const order_id = `M${++orderSeq}`;
        book.set(order_id, {
          order_id,
          market: order.market,
          side: order.side,
          odds: order.odds,
          stake: order.stake,
          cycle: cycleAt(Date.now()),
          fill_ts: Date.now(),
        });
        socket.send(JSON.stringify({
          status: "filled",
          order_id,
          market: order.market,
          side: order.side,
          odds: order.odds,
          stake: order.stake,
          fill_ts: Date.now(),
        }));
        console.log(`[mock-vendor] ORDER FILLED -> ${order_id}`);
        return;
      }
      socket.send(JSON.stringify({ status: "error", body: "unknown frame" }));
    });
  });
  return wss;
}

vendorServer();
tradeServer();

process.on("SIGTERM", () => process.exit(0));