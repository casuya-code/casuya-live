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
| `web-interface-js/backend` | Node.js | WebSocket relay from Redis pub/sub | Railway Web |
| `web-interface-js/frontend` | Next.js/React | Live operational dashboard | Vercel |
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

## Testing

```powershell
go test -vet=off ./...   # in each data-ingestion-go / execution-engine-go
python tests/smoke_test.py   # in analytics-engine-py
npm run build --prefix web-interface-js/frontend
```

CI (`.github/workflows/ci.yml`) runs all of the above on GitHub, where `go vet`
is not App-Control-blocked, so `go test ./...` is used there.

## Security notes

- Execution payloads are signed (HMAC-SHA256, base64url without padding).
  `client_test.go` pins that contract, including rejecting padded signatures.
- `INTERNAL_AUTH_SECRET` is `local-dev-secret` for local dev only; change it and
  `BOOKMAKER_API_KEY` for any real deployment.
- The executor HTTP endpoint validates `X-Internal-Token` before touching the broker.