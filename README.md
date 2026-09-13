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

## Environment variables

- `REDIS_URL` — internal Redis connection string (Railway provisioned)
- `PROVIDER_WS_URL` — vendor live-data WebSocket endpoint
- `BOOKMAKER_API_KEY` — bookmaker trade-execution secret (execution engine)
- `BOOKMAKER_WS_URL` — bookmaker execution WebSocket endpoint (execution engine)
- `INTERNAL_AUTH_SECRET` — HMAC key signing analytics->execution payloads
- `NEXT_PUBLIC_WS_BACKEND_URL` — public web-relay URL for the frontend

See `.env.example` at the repo root for local development shorthand.

## Local development

Each service runs independently. The Go services require a running Redis
(`REDIS_URL`); the analytics engine additionally needs the calibration assets
in `analytics-engine-py/models/calibration`.