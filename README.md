# CASUYA-LIVE

Decoupled, event-driven trading platform that collects live sports data, evaluates
value opportunities targeting high odds (>= 5.0), triggers microsecond bet executions,
and runs an automated AI feedback loop to tune predictive parameters.

## Services

| Service | Language | Role | Deploy target |
|---|---|---|---|
| `data-ingestion-go` | Go | Streaming data consumer (WS/SSE) -> Redis | Railway Worker |
| `analytics-engine-py` | Python | Study Room, filter engine, diagnostics, AI self-correction | Railway Worker |
| `execution-engine-go` | Go | Signed command listener, direct bookmaker execution | Railway Worker (private mesh) |
| `web-interface-js/backend` | Node.js | WebSocket relay from Redis pub/sub (public + admin tiers) | Railway Web |
| `web-interface-js/frontend` | Next.js/React | Public live dashboard + `/admin` operator console | Vercel |
| `mock-vendor` | Node.js | Local simulator: vendor WS feed + bookmaker WS (no external creds) | local only |

## Data flow

```
provider WS ─▶ data-ingestion-go ─▶ Redis (hash/stream) ─▶ analytics-engine-py
                                                                 │
                                                signed payload   │
                                                       ▼         ▼
                              web relay ◀───── Redis pub/sub ─ execution-engine-go
                                   │                          (bookmaker WS)
                                   ▼
                         frontend dashboard
```

Locally, the provider and bookmaker are the `mock-vendor` simulator; swap
`PROVIDER_WS_URL` / `BOOKMAKER_WS_URL` for real endpoints in production.

## Environment variables

- `REDIS_URL` — internal Redis connection string
- `PROVIDER_WS_URL` — vendor live-data WebSocket endpoint (`ws://` locally, `wss://` in prod)
- `BOOKMAKER_API_KEY` — bookmaker trade-execution secret (execution engine)
- `BOOKMAKER_WS_URL` — bookmaker execution WebSocket endpoint
- `INTERNAL_AUTH_SECRET` — HMAC key signing analytics->execution payloads
- `EXECUTION_SERVICE_URL` — execution engine HTTP endpoint (analytics dispatch)
- `MOCK_VENDOR_PORT` / `MOCK_TRADE_PORT` — mock simulator listen ports
- `NEXT_PUBLIC_WS_BACKEND_URL` — public web-relay URL for the frontend

See `.env.example` at the repo root for the local defaults.

## Local development

### One command (Windows)

Runs Redis, web relay, analytics, mock vendor, ingestor, executor and the
dashboard with sensible local defaults, then health-checks each service.

```powershell
.\start-local.ps1
```

Open the dashboard at http://localhost:3000. Logs land in `.tools\logs\`.
Stop everything with:

```powershell
.\start-local.ps1 -Stop
```

First-time setup:

1. Go (1.26+) in `GOROOT`, and build the binaries into `.tools\bin`:
   `go build -o ../.tools/bin ./...` inside each `*-go` directory.
2. Native Windows Redis at `.tools\redis` (or any Redis on `localhost:6379`).
3. Python venv at `analytics-engine-py\.venv` with `requirements.txt` installed.
4. `npm install` in `web-interface-js\backend` and `web-interface-js\frontend`.

The mock vendor streams a repeating Example FC vs Casual United fixture where the
away 2H price drifts to >= 5.0 — enough to trigger real orders that the mock
bookmaker fills — plus a `FULLTIME` settlement at the end of every cycle.

### Per-service

Each service also runs on its own. The Go services require a running Redis
(`REDIS_URL`); the analytics engine additionally needs the calibration assets in
`analytics-engine-py/models/calibration`.

### Calibration

The win-probability weights are fitted offline from the durable frame history
(`matches:live` stream entries carry the full frame under `data`):

```powershell
python analytics-engine-py/scripts/calibrate.py --frames 2000 --update-distributions [--l2 1e-3]
```

It regroups frames into complete match cycles, replays them through the study
using each frame's own `received_at` timestamp (so offline folding matches the
live windows exactly), builds home/away training rows labeled by the final
score, splits chronologically (80% training / 20% validation by cycle), and
fits a pure-stdlib logistic regression with an L2 ridge penalty over the exact
`FilterEngine.score_inputs` vector the live filter consumes. The adoption gate
rejects the fit (shim stays in force) when history is too thin, the features
carry no signal, or the model's validation logloss is no better than the naive
baseline. `--update-distributions` recomputes the per-league
`distributions.json` baselines (danger, possession, shot accuracy) from the
observed frames.

Each run also regenerates the reference datasets shipped with the repo:
`decay_matrix.csv` (time-decay across the study window's minute buckets),
`season_trends.csv` (per-league outcome rates plus minute-band baselines), and
appends one drift-snapshot row to `calibration_timeseries.csv`. Fit health is
mirrored to the redis `calibration:model` hash so the dashboard's Model
Accuracy panel can render it via the relay's `GET /api/stats`.

## Testing

```powershell
go test ./...   # in each data-ingestion-go / execution-engine-go (vet runs locally now)
python tests/test_calibrate.py   # in analytics-engine-py (fit, metrics, gate)
python tests/test_feature_study.py   # in analytics-engine-py (replay parity, monotonicity, windows)
python tests/smoke_test.py   # in analytics-engine-py (end-to-end pipeline)
REDIS_URL=redis://localhost:6379/0 node test_stats.js   # in web-interface-js/backend (admin-tier API)
REDIS_URL=redis://localhost:6379/0 node test_ws_roles.js   # in web-interface-js/backend (WS role tiering)
npm run build --prefix web-interface-js/frontend
```

CI (`.github/workflows/ci.yml`) runs the same suite: Go build+vet+test, the
Python calibrate/feature-study/smoke tests, the web-relay stats + WS-role
tests against a Redis service container, and the frontend build.

## Admin tier (relay / admin dashboard)

WebSocket/HTTP access to the relay is split into two tiers at the relay:

- **Public** — `/ws` clients without a token only ever receive the live match
  feed (`matches:live`). The operator data streams and the bankroll stats are
  never routed to public sockets; the relay enforces this per-socket (it is not
  a client-side convention).
- **Admin** — clients that upgraded `/ws?token=<session>` also receive
  `execution:pnl` settlements, `diagnostics:events` audits, and the
  HMAC-signed `execution:commands` bus. `GET /api/stats` (calibration
  weights/metrics, session bankroll, outcomes ledger, stream staleness)
  requires the same bearer token.

Session tokens are issued by `POST /api/auth/token` with the pre-shared
`ADMIN_SECRET` (timing-safe compare). The token is a short-lived (8h)
`base64url(payload) . HMAC-SHA256(payload, ADMIN_SECRET)` envelope mirroring the
repo's signed-frame format. `ADMIN_SECRET` is set in `start-local.ps1`
(`local-admin-secret` for local dev only — change it for real deployments).

Frontend: the public dashboard at `/` (study room / live grid) is token-free.
The operator console at `/admin` runs a login gate (`AdminLogin`), then mounts
the Execution P&L, Model Accuracy, Execution Commands, and Diagnostics panels
over an admin-upgraded WebSocket. Both Hooks refuse to connect without a valid
session, and the session token clears on expiry or logout.

## Security notes

- Execution payloads are signed (HMAC-SHA256, base64url without padding).
  `client_test.go` pins that contract, including rejecting padded signatures.
- `INTERNAL_AUTH_SECRET` is `local-dev-secret` for local dev only; change it and
  `BOOKMAKER_API_KEY` for any real deployment.
- The executor HTTP endpoint validates `X-Internal-Token` before touching the broker.
- `execution:commands` is wired into the relay strictly as an admin channel:
  public sockets are never offered it. `/api/stats` is 401 without a session
  token and 503 when the admin tier is not configured.
- Admin session tokens are bearer credentials scoped to the relay process;
  persist only the 8h token in browser storage, never `ADMIN_SECRET` itself.