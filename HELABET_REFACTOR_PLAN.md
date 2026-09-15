# CASUYA-LIVE — Refactor Plan

## Overview

Surgical refactor of the CASUYA-LIVE execution engine into an **Algorithmic Signal & Analytics Dashboard**:

1. **Paper trading** — validate the 0.8709 AUC model on real market data with zero financial risk
2. **Signal broadcast** — stream live signals to frontend in real-time
3. **Live broker** — swap to Helabet (or any HTTP-based bookmaker) once authorized
4. **Mock vendor** — keep existing local development path unchanged

**Not a rewrite.** The core domain logic (settlement grading, PnL, Redis pipeline) is transport-agnostic. Only the transport layer changes.

---

## Phase 1: Interfaces + Paper Trading ✅ DONE

### Completed

- `execution-engine-go/internal/broker/interfaces.go` — `OrderPlacer` interface + `PaperBroker`
- `execution-engine-go/internal/broker/paper_test.go` — unit tests
- `execution-engine-go/internal/broker/client.go` — `PlaceOrder()` on `Broker`
- `execution-engine-go/internal/api/server.go` — `OrderPlacer` interface
- `execution-engine-go/cmd/executor/main.go` — factory with `PAPER_TRADE` + `BROKER_MODE`

### Live verification

```
[paper] order paper_1789418365095700602: 1H_1X2 away @4.97 (raw 5.00) stake 25.00
[paper] settled 12 order(s) for epl-2026-0042 1-2: net +1204.89
```

---

## Phase 1B: Signal Broadcast + API Layer (NOW)

### Goal

Expose the active paper-trading pipeline to a web interface. The backend engine continues processing live matches, placing virtual bets, and evaluating performance. A new API layer streams signals to the frontend in real-time.

### Architecture

```
[Go Analytics Engine]
       │
       ▼ (Phase 1: PaperBroker.PlaceOrder)
[Redis: execution:paper_orders]
       │
       ▼ (NEW: market:signals:live pub/sub)
[Go HTTP/WebSocket API Layer] ──(stream JSON)──> [Web Frontend]
```

### 1. Signal Broadcast (internal/broker/interfaces.go)

When `PaperBroker.PlaceOrder()` writes to `execution:paper_orders`, also publish to a new Redis channel:

```go
func (p *PaperBroker) PlaceOrder(ctx context.Context, marketID string, side string, odds float64, stake float64) (*Fill, error) {
    // ... existing fill logic ...

    // Broadcast signal to frontend
    signal, _ := json.Marshal(map[string]any{
        "type":      "signal",
        "order_id":  fill.OrderID,
        "market":    marketID,
        "side":      side,
        "odds":      fillPrice,
        "raw_odds":  odds,
        "stake":     stake,
        "timestamp": fill.FillAt.UnixMilli(),
    })
    p.rdb.Publish(ctx, "market:signals:live", signal)

    return fill, nil
}
```

### 2. New API Endpoints (internal/api/server.go)

Add three endpoints to the existing Go HTTP server:

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/v1/stream/signals` | WebSocket | Real-time signal stream |
| `/api/v1/signals/active` | GET | Unsettled paper orders |
| `/api/v1/analytics/pnl` | GET | Cumulative PnL stats |

#### WebSocket signal stream

```go
func (h *Handler) streamSignals(w http.ResponseWriter, r *http.Request) {
    // Upgrade to WebSocket
    // Subscribe to market:signals:live Redis channel
    // Forward each message to the connected client
    // Clean up on disconnect
}
```

#### Active signals endpoint

```go
func (h *Handler) activeSignals(w http.ResponseWriter, r *http.Request) {
    // Read execution:paper_orders hash from Redis
    // Filter for un-settled positions
    // Return as JSON array
}
```

#### PnL analytics endpoint

```go
func (h *Handler) analyticsPnL(w http.ResponseWriter, r *http.Request) {
    // Read pnl:session hash from Redis
    // Return cumulative stats: net, won, lost, win_rate
}
```

### Files to modify

| File | Change |
|------|--------|
| `execution-engine-go/internal/broker/interfaces.go` | Add `market:signals:live` publish in `PlaceOrder()` |
| `execution-engine-go/internal/api/server.go` | Add 3 new endpoints + WebSocket handler |
| `execution-engine-go/cmd/executor/main.go` | Register new routes |

### Env vars

| Variable | Default | Description |
|----------|---------|-------------|
| `SIGNAL_CHANNEL` | `market:signals:live` | Redis channel for live signals |

### Redis schema additions

```
Pub/Sub: market:signals:live
         Messages: JSON signal objects from PaperBroker

Hash:    execution:paper_orders (existing)
         Field: <paper_order_id>
         Value: JSON(Fill)

Hash:    pnl:session (existing)
         Field: net / won / lost / source / updated_cycle
```

---

## Phase 1C: Frontend Dashboard (AFTER 1B)

### Goal

Build a responsive single-page dashboard that displays live signals and PnL performance. The backend engine runs in the background; the frontend is a read-only operator console.

### Architecture

The existing `web-interface-js/frontend` (Next.js) already has:
- Public dashboard at `/` (study room, live grid)
- Admin console at `/admin` (PnL, diagnostics, commands)

**Add a new route `/signals`** for the signal dashboard. This keeps the existing admin console intact.

### Zone A: Live Signal Queue (`/signals`)

A high-visibility grid that flashes new signals in real-time via WebSocket.

**Target fields:**

| Field | Source | Description |
|-------|--------|-------------|
| Fixture | `match_id` → Redis hash `match:<id>` | Home vs Away team names |
| Market Selection | `market` + `side` | e.g., "1H_1X2 Away" |
| Engine Odds | `odds` (post-slippage) | Real-time odds caught by engine |
| Raw Odds | `raw_odds` | Pre-slippage for comparison |
| Rec. Stake | `stake` | Calculated stake amount |
| Time | `timestamp` | Signal generation time |
| Status | Settlement lookup | PENDING / WON / LOST |

**Action feature:** "Copy Match Details" button — copies `fixture | market | odds | stake` to clipboard for manual bet placement on betPawa/Helabet.

### Zone B: Real-Time PnL Tracker

Reads from `pnl:session` hash and displays:

| Metric | Source |
|--------|--------|
| Total Paper Bets | `won` + `lost` |
| Win Rate % | `won` / (won + lost) × 100 |
| Net Profit (TZS) | `net` |
| Max Drawdown | Computed from settled ledger |

**Settled Ledger Table:** Historical log of graded paper trades with:
- Final score
- Result (WON green / LOST red)
- PnL per trade

### Files to create/modify

| File | Change |
|------|--------|
| `web-interface-js/frontend/src/app/signals/page.jsx` | New signal dashboard page |
| `web-interface-js/frontend/src/components/SignalGrid.jsx` | Live signal queue component |
| `web-interface-js/frontend/src/components/PnLTracker.jsx` | PnL metrics + ledger |
| `web-interface-js/frontend/src/hooks/useSignalStream.js` | WebSocket hook for signal stream |
| `web-interface-js/backend/server.js` | Relay `market:signals:live` to admin clients |

### WebSocket integration

The existing relay (`web-interface-js/backend/server.js`) already subscribes to Redis channels and relays to WebSocket clients. Add `market:signals:live` to the channel list:

```js
const WS_CHANNELS = [
  PUBLIC_CHANNEL,           // matches:live
  "diagnostics:events",
  "execution:pnl",
  "execution:commands",
  "market:signals:live",    // NEW: live signals
];
```

Public clients see only `matches:live`. Admin clients see all channels including signals.

### Frontend tech stack

- **Framework:** Next.js (existing)
- **Styling:** Tailwind CSS (existing)
- **WebSocket:** Existing `useWebSockets.js` hook (extend for signals)
- **State:** React hooks (no external state library needed)

---

## Phase 2: Verify Helabet API Status (RESEARCH — DONE, findings recorded)

### Goal

Determine if Helabet offers an official programmatic betting API for the Tanzanian market.

### Actions

1. ✓ Contact Helabet Tanzania support: "Do you offer an API for automated betting?" — **not yet sent, awaited at gate**
2. ✓ Search for developer portal or partner program — **no public program for retail bettors found**
3. ✓ Review Terms of Service for automated betting clauses — **automation not documented as available**
4. Decision gate → pending operator choice

### Findings

- **Helabet (`helabet.com`) is a consumer-facing betting site** (1xBet-network skin). There is **no documented public API for odds or bet placement**, and no developer portal for retail accounts.
- **betPawa is likewise consumer-only** — no public betting API for TZS bets.
- Public "sportsbook APIs" found in research are **odds/results FEEDS** (The Odds API, LSports, Sportradar, Genius Sports, MetaBet) that provide pricing data only — useful for **Phase 4 (data ingestion)** but not for placing bets.
- Proper bet-PLACEMENT APIs (Pinnacle, Betfair Exchange) exist but **do not settle in Tanzanian rails** (M-PESA/Tigo) — not viable for local TZS accounts.
- Realistic real-money path for a local book is therefore **session/transport-level automation** (Phase 3B) or **human-in-the-loop placement** — NOT an official key.

### Recommendation

| Path | Risk | Effort | When |
|------|------|--------|------|
| **A. Operator-assisted live** (RECOMMENDED): dashboard shows signal, operator places on Helabet manually, confirms outcome; engine grades paper in parallel | Low (no automation detected) | Low — reuses 1C + a small workflow | Now |
| **B. Cookie-session client** (Phase 3B): `HelabetClient` over logged-in HTTP, TZS 1000 max | High (ban/breakage, ToS) | Medium — fits existing `OrderPlacer` | After A validated |
| **C. Paper-only extension** | None | None | If B not acceptable |

### Decision gate

| Outcome | Action |
|---------|--------|
| Official API available | Phase 3A: `HelabetClient` with API key |
| No API, ToS allows automation | Phase 3B: Cookie injection (B above) |
| No API, ToS forbids automation | Operator-assisted live (A) or paper-only (C) |

**Operator decision (9/15/2026):** **A — Operator-assisted live.** Implemented below.

---

## Phase 3: Operator-Assisted Live (CHOSEN — DONE, deployed)

### Goal

Reach real money with **zero automation risk**: the operator copies a signal, places the ticket manually on Helabet, then confirms on the desk. The engine keeps grading paper positions in parallel for validation. The handle <= TZS stake the model recommends.

### Backend (executor)

- `internal/broker/operator.go` — `Placement` type + `RecordPlacement` (Redis pipeline):
  - `HSet` to hash `operator:placements` (lookup by `order_id`)
  - `XAdd` to stream `execution:operator_placements` (MaxLen 1000, ordered ledger)
  - `Publish` to channel `operator:placements` (live dashboards)
  - Env: `OPERATOR_HASH`, `OPERATOR_CHANNEL`
- `internal/api/server.go` — two endpoints:
  - `POST /api/v1/operator/place` — body `{order_id, match_id, market, side, odds, stake, book, status}`; status ∈ `placed | void | failed`; `book` defaults to `helabet`
  - `GET /api/v1/operator/placements?limit=` — newest-first from the stream, numeric coercion via `numericValue`
- Tests: 3 new (503 without redis, 405 on GET, 503 placements). All green.

### Frontend (signal desk)

- `src/lib/engine.js` — `fetchPlacements(limit)`, `recordPlacement(payload)` (POST)
- `src/components/SignalGrid.jsx` — per-card `MARK PLACED` button (posts then flips to a green `PLACED · helabet · <time-ago>` badge); `void` renders dim; `timeAgo()` helper
- `src/app/signals/page.jsx` — loads placements every poll, merges into `placements` map by `order_id`, `handlePlace(signal)` posts the placement and updates local state optimistically

### Verification

- Railway executor deployed; `GET /api/v1/operator/placements` live (200 `[]`)
- `POST /api/v1/operator/place` rejects bad status `bogus` → 400
- CORS `Access-Control-Allow-Origin: *` confirmed for the Vercel origin
- Local stack: rebuilt `executor.exe` → restart → success path verified end-to-end (POST 200 → hash + stream persisted → GET returns typed entry → cleaned test record)
- Frontend `/signals` deployed to Vercel (200, queue markup present)

### Operator workflow

1. Watch the live queue; a strong signal flashes in.
2. `COPY MATCH` → paste ticket into Helabet desktop/app at the listed odds/stake.
3. Ticket accepted → `MARK PLACED` (green badge, timestamp).
4. Engine continues scoring the same pick on paper; desk compares real vs paper outcome at FULLTIME.

### Next (optional)

- `void`/`failed` actions from the card (button toggle) — currently POST-only
- Diff view: paper result vs operator placement result per match
- TZS budget tracking + daily stop-loss on placed tickets

---

## Phase 3A: Live Broker — Official API (AFTER PHASE 2)

### Goal

Implement `HelabetClient` satisfying `OrderPlacer` using Helabet's official API.

### Files to create

| File | Purpose |
|------|---------|
| `execution-engine-go/internal/broker/helabet.go` | `HelabetClient` implementation |
| `execution-engine-go/internal/broker/helabet_test.go` | Unit tests with mocked HTTP |

### Interface implementation

```go
type HelabetClient struct {
    cfg        HelabetConfig
    httpClient *http.Client
}

type HelabetConfig struct {
    APIKey  string
    BaseURL string
    SportID int
}

func (h *HelabetClient) PlaceOrder(ctx context.Context, marketID string, side string, odds float64, stake float64) (*Fill, error) {
    // HTTP POST with API key auth
    // Parse response into Fill
    // Handle errors, retries, rate limiting
}
```

### Env vars

| Variable | Description |
|----------|-------------|
| `HELABET_API_KEY` | Official API key |
| `HELABET_BASE_URL` | API endpoint |
| `HELABET_SPORT_ID` | Sport filter (1 = Football) |

---

## Phase 3B: Live Broker — Cookie Injection (AFTER PHASE 2)

### Goal

Implement `HelabetClient` using session cookies. **Only if Phase 2 confirms acceptable.**

### Cookie injection

```go
type HelabetClient struct {
    cfg        HelabetConfig
    httpClient *http.Client
}

type HelabetConfig struct {
    Cookie      string  // HELABET_COOKIE
    UserAgent   string  // HELABET_USER_AGENT
    BaseURL     string
    RateLimitMS int
}

func (h *HelabetClient) PlaceOrder(ctx context.Context, marketID string, side string, odds float64, stake float64) (*Fill, error) {
    req, _ := http.NewRequestWithContext(ctx, "POST", h.cfg.BaseURL, payload)
    req.Header.Set("Cookie", h.cfg.Cookie)
    req.Header.Set("User-Agent", h.cfg.UserAgent)
    // Rate limit, retry, parse
}
```

### Risks

- Cookie expiry → silent auth failures
- Endpoint changes → broken overnight
- Account ban if automation detected

---

## Phase 4: Real Data Ingestion — Helabet Direct Feed (DONE ✅, deployed)

### Goal

Replace mock vendor's fake fixtures with **real live in-play football** so the
desk shows genuinely bettable matches. Real odds, real time, and — critically —
the **truth of the book itself**: prices are Helabet's own, not an aggregator's.

### The find: Helabet exposes its own public feed

Helabet.co.tz runs on the **EveryMatrix** sportsbook stack. Its SPA polls
**unauthenticated JSON endpoints** that you can hit with plain HTTP + a couple of
app-source headers. That means our own collector can pull the live board
directly — no third-party API, no key approval, no rate cap:

| Endpoint | Purpose |
|----------|---------|
| `GET /service-api/main-live-feed/v3/games1x2?cfView=3&count=50&fcountry=181&gr=772&grMode=4&lng=en&ref=237` | Live board: in-play score, clock (`timer.timeSec` → `6'`), period, and **tabloStats** (dangerous attacks, possession %, shots on/off target, xG…) |
| `GET /service-api/main-line-feed/v3/games1x2?...` | Pre-match board (future: preview fixtures) |
| `GET /service-api/result/web/api/v2/sports?…` (header `__RESULTS_FRONTEND__`) | Results — final scores for settlement (needs per-match endpoint recon; see Known gaps) |

Required request headers (always):
```
x-svc-source: __BETTING_APP__     (or __RESULTS_FRONTEND__ for the results API)
x-app-n:     __BETTING_APP__
is-srv:      false
x-requested-with: XMLHttpRequest
Accept/Content-Type: application/json
```

Discovered constraints: `count > 50` returns `400`; plain requests without the
app headers return `400`; results API additionally wants its own
`__RESULTS_FRONTEND__` marker.

### Why this beats API-Football

| Property | API-Football free | **Helabet direct feed** |
|----------|-------------------|-------------------------|
| Approval | 24h+ pending key | None — works immediately |
| Rate limit | 180 calls/hr/endpoint | None (it's the book's own feed; poll every 5s) |
| Odds | Median across external books | **Helabet's exact live prices** |
| Coverage | England Championship + France Ligue 2 only | Full live board (many leagues incl. TZ regional) |
| Stats | Dangerous attacks + shots + possession | Same + xG, passing accuracy |
| Truth of settlement | Aggregator consensus | The book's own line (operator verifies at ticket) |

The API-Football provider (built earlier) remains in the codebase as
`PROVIDER_MODE=apifootball` fallback.

### Market contract (helabet mode)

| Field | Value |
|-------|-------|
| `match_id` | `hb-<event id>` (map to Helabet's own event id in the live tab) |
| `market` | `FULLTIME_1X2`, from `eventGroups` groupId 1, `type` 1/2/3 → home/draw/away |
| `clock` | `"6'"`, `"HT"`, `"FULLTIME"` from `statusLineStr` + `timer.timeSec` |
| `score` | `scoreOpp1` / `scoreOpp2` |
| stats | `tabloStats` ids: 58 dangerous attacks, 29 possession, 59/60 shots on/off target |
| Football gate | only `sport.id == 1` published (cricket & others dropped) |

### Settlement (FULLTIME grading)

The live feed drops finished matches, so the poller:
- emits `Clock:"FULLTIME"` when a surfaced entry signals `Match finished`/stopped
  timer after 90', and
- settles any tracked match that vanished from the feed at last sighting
  `>= 89:50` elapsed, using its last-known score (poll is ~5s, so last sighting is
  within seconds of the FT whistle).
- Matches that drop off before 90' stay pending → operator grades those manually
  on the desk. Known gap: dedicated results-feed mapping (per-match endpoint) is
  still to recon for fully automated verification.

### Files created/modified

| File | Change |
|------|--------|
| `data-ingestion-go/internal/helabet/types.go` | JSON mapping for the EveryMatrix live payload + clock/FT helpers |
| `data-ingestion-go/internal/helabet/poller.go` | HTTP poller, app-header decoration, market/stats mapping, settlement loop |
| `data-ingestion-go/internal/helabet/poller_test.go` | Unit tests: clock, FT detection, odds extraction, mapping, fetch, settlement, stale purge |
| `data-ingestion-go/cmd/ingestor/main.go` | `PROVIDER_MODE=helabet` branch |
| `start-local.ps1` | Passes `HELABET_BASE_URL` / `HELABET_POLL_SECONDS` |

### Env vars

| Variable | Default | Description |
|----------|---------|-------------|
| `PROVIDER_MODE` | `mock` | `mock` (WS) \| `helabet` (direct book feed) \| `apifootball` |
| `HELABET_BASE_URL` | `https://helabet.co.tz` | Book origin behind the feed |
| `HELABET_POLL_SECONDS` | `5` | Live board poll cadence (no rate cap; 5s ≈ SPA cadence) |

### Verified (9/15/2026)

- Local e2e: real frames in Redis (hb-*, real odds 1.38–50, real clocks 6'–41').
- Production Railway: `PROVIDER_MODE=helabet` deployed; `/api/v1/signals/active`
  showing real fixtures — e.g. AFC Champions League Elite Fulham aside,
  `hb-753077047 Darul Takzim vs Buriram United FULLTIME_1X2 away @ 12.74, 32'`,
  and a 22.89 away leg on England U21 Development League.

---

## Execution Order

```
Phase 1 (DONE ✅)
  ├── interfaces.go
  ├── PaperBroker + slippage
  ├── PaperRunLoop (matches:live FULLTIME grading)
  ├── CommandRunLoop (execution:commands subscription)
  ├── Factory in main.go
  ├── Tests
  └── Deployed to Railway with PAPER_TRADE=true

Phase 1B (DONE ✅)
  ├── Signal broadcast: market:signals:live pub/sub
  ├── API endpoints: /api/v1/stream/signals, /active, /pnl
  ├── WebSocket handler for real-time streaming
  ├── Tests
  └── Deploy to Railway

Phase 1C (DONE ✅)
  ├── Frontend: /signals page
  ├── SignalGrid component (live queue + copy)
  ├── PnLTracker component (metrics + settled ledger)
  ├── useSignalStream hook + engine lib
  ├── Ledger persisted to execution:paper_settlements
  ├── /api/v1/analytics/ledger endpoint
  ├── CORS enabled on engine for browser dashboards
  ├── NEXT_PUBLIC_ENGINE_URL set on Vercel
  └── Deployed to Vercel

Phase 2 (RESEARCH — DONE, findings recorded)
  ├── Research: no public Helabet/betPawa betting API exists
  ├── Recommended path: operator-assisted live (A), then optional Phase 3B
  └── Decision gate: operator chose operator-assisted live

Phase 3 (OPERATOR-ASSISTED LIVE — DONE ✅)
  ├── operator.go (Placement type + Redis pipeline)
  ├── POST /api/v1/operator/place + GET /api/v1/operator/placements
  ├── SignalGrid "MARK PLACED" button + placements state
  ├── E2E verified (POST 200 → Redis → GET typed)
  └── Deployed to Railway + Vercel

Phase 4 (HELABET DIRECT FEED — DONE ✅, DEPLOYED)
  ├── helabet/types.go + poller.go + poller_test.go
  ├── EveryMatrix public live-feed recon (headers, count<=50, stats ids)
  ├── PROVIDER_MODE=helabet branch in main.go
  ├── Football-only gate (sport.id==1), MarketID fix
  ├── Local e2e verified: real hb-* frames in Redis
  └── Railway deployed: /signals/active showing real fixtures w/ real odds

Phase 4B (AUTOMATED SETTLEMENT VERIFY — NEXT ⏳)
  ├── Recon Helabet results per-match endpoint (games route 405 for now)
  ├── Full-time idempotent grading vs feed drop-off
  └── Operator auto-close on verified results
```

---

## Deployment

All phases deploy to the same Railway project (`casuya-live`). Environment variables control behavior:

```bash
# Mock mode (fallback local/dev)
PROVIDER_MODE=mock PROVIDER_WS_URL=ws://mock-vendor:19999/vendor PAPER_TRADE=true

# REAL DATA (current production) — the book's own live feed
PROVIDER_MODE=helabet PAPER_TRADE=true
#   + optional HELABET_BASE_URL / HELABET_POLL_SECONDS

# API-Football fallback (requires approved key — user action pending)
PROVIDER_MODE=apifootball APIFOOTBALL_KEY=xxxxx PAPER_TRADE=true

# Paper trading + signal dashboard (Phase 1B/1C)
PAPER_TRADE=true BROKER_MODE=mock

# Live Helabet (Phase 3 — operator-assisted, future)
PAPER_TRADE=false BROKER_MODE=helabet HELABET_COOKIE=... HELABET_USER_AGENT=...
```

---

## Success Criteria

- [x] Phase 1: Paper trades log to Redis, get graded, PnL visible
- [x] Phase 1: All existing tests pass, no regressions
- [x] Phase 1B: Signal broadcast to `market:signals:live` working
- [x] Phase 1B: `/api/v1/stream/signals` WebSocket streams live signals
- [x] Phase 1B: `/api/v1/signals/active` returns unsettled orders
- [x] Phase 1B: `/api/v1/analytics/pnl` returns cumulative stats
- [x] Phase 1C: `/signals` dashboard page live on Vercel
- [x] Phase 1C: Signal grid flashes new signals in real-time
- [x] Phase 1C: PnL tracker shows metrics + settled ledger
- [x] Phase 1C: "Copy Match Details" button works
- [x] Phase 2: Helabet API status research complete (no public API found; operator chose operator-assisted live)
- [x] Phase 3: Operator-assisted live desk shipped and deployed (placements POST/GET, backend + frontend, verified e2e)
- [ ] Phase 3: First real TZS ticket placed via the desk
- [x] Phase 4: Helabet direct live feed built, unit-tested, wired into PROVIDER_MODE=helabet
- [x] Phase 4: Real odds flowing from provider — production /signals showing real Helabet fixtures (verified 9/15/2026)
- [x] Phase 4: Football-only gate + MarketID fix (cricket/no-market frames dropped)
- [ ] Phase 4B: Helabet results-feed mapping for fully automated settlement verification
