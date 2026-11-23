# Leaderboard Service

A game leaderboard HTTP service in Go, backed by Redis. Portfolio project for a
backend role (Go, goroutines/channels, Redis, Docker, CI/CD).

## How to work with me

- Talk to me in Turkish. Write code, comments, commit messages and README in English.
- I am new to Go (background: C#, ASP.NET Core, Node.js). After each phase, explain
  what you built in Turkish, file by file, and point out Go idioms that differ from C#.
- Work one phase at a time (see "Phases"). Do not start the next phase until I say so.
- Plan first, then implement. Keep changes small and commit after each working step.
- Before saying a step is done, run: `gofmt -l .` (must print nothing), `go vet ./...`,
  `go test -race ./...`. If something fails, fix it or tell me plainly.
- Never invent numbers. Benchmark results in README must be copied from real command
  output, with date, machine (CPU, RAM) and the exact command.
- Never commit secrets. Real values go in `.env` (gitignored); keep `.env.example`.
- Ask before adding a new dependency. Prefer the standard library.

## Stack

- Go 1.27, standard library `net/http` router (method + path patterns, `r.PathValue`)
- Redis 8 via `github.com/redis/go-redis/v9`
- Logging: `log/slog` (JSON). Config: environment variables only.
- No web framework, no ORM, no DI container. Wire dependencies by hand in `main.go`.

## Layout

```
cmd/server/main.go        config, wiring, HTTP server, graceful shutdown
cmd/loadgen/main.go       load generator (phase 4)
internal/api/             handlers, middleware (API key, logging), JSON helpers
internal/leaderboard/     domain types, Service, Store interface, MemoryStore
internal/store/redisstore/ Store implementation on Redis
internal/ingest/          buffered channel + worker pool + batched writes
internal/config/          env parsing with defaults and validation
docs/decisions.md         short design decision records
```

## API

All JSON. Errors: `{"error": "message"}` with a proper status code.

| Method and path | Behaviour |
| --- | --- |
| `POST /v1/leaderboards/{board}/scores` | Body `{"player_id","score"}`. Requires `X-API-Key`. Async mode: enqueue, 202; queue full: 503 + `Retry-After: 1`. Sync mode: write, 200. |
| `GET /v1/leaderboards/{board}/top?limit=10` | Top N with rank (1-based), player_id, name, score. limit 1..100, default 10. |
| `GET /v1/leaderboards/{board}/players/{player}` | `{rank, score}`. 404 if not on the board. |
| `GET /v1/leaderboards/{board}/players/{player}/around?radius=5` | Players around this one. radius 1..50. Clamp at list edges. |
| `PUT /v1/players/{player}` | Body `{"name"}`. Requires `X-API-Key`. |
| `GET /healthz` | Liveness, always 200 while the process runs. |
| `GET /readyz` | 200 if Redis `PING` succeeds within 500 ms, else 503. |

Validation: board and player IDs match `^[a-zA-Z0-9_-]{1,64}$`; score is a
non-negative integer up to 1e12. Return 400 with a clear message otherwise.

## Redis keys

- `lb:{board}` sorted set: member = player_id, score = score.
- Write: `ZADD lb:{board} GT score player` ("best score" mode; retries are idempotent).
- Top N: `ZRANGE lb:{board} 0 N-1 REV WITHSCORES`, then names via one pipeline of `HGET`.
- Rank: `ZREVRANK` (+1) and `ZSCORE`. Around: rank r, then `ZRANGE ... REV` for r-k..r+k.
- `player:{id}` hash: field `name`.
- Every Redis call gets a context with a timeout (`REDIS_TIMEOUT`, default 200ms).
- Known limitation (document in README): equal scores are ordered by member ID.

## Ingest (async write path)

- `chan ScoreEvent` with capacity `QUEUE_SIZE`. Handler does a non-blocking send
  (`select` with `default`); if full, return 503.
- `WORKERS` goroutines read from the channel. Each worker batches until `BATCH_SIZE`
  events or `FLUSH_INTERVAL` elapses (`select` on channel + `time.Ticker`), then writes
  the batch in one Redis pipeline.
- Shutdown order: `signal.NotifyContext` -> `http.Server.Shutdown` (stop accepting) ->
  `close(ch)` -> workers drain and flush -> `WaitGroup.Wait()` -> exit.
- Only the owner (the ingest package) closes the channel. No goroutine may outlive shutdown.
- Document the trade-off: an accepted (202) score that is still in memory is lost if the
  process crashes. `INGEST_MODE=sync` skips the queue.

## Config (env)

`PORT` (8080), `REDIS_ADDR` (localhost:6379), `REDIS_PASSWORD` (empty), `API_KEY`
(required), `INGEST_MODE` (async|sync, default async), `QUEUE_SIZE` (1024),
`WORKERS` (4), `BATCH_SIZE` (100), `FLUSH_INTERVAL` (50ms), `REDIS_TIMEOUT` (200ms),
`SHUTDOWN_TIMEOUT` (10s). Fail fast at startup on invalid values.

## Code conventions

- Wrap errors with context: `fmt.Errorf("zadd %s: %w", key, err)`; check with `errors.Is`.
- Accept `context.Context` as the first parameter of anything that does I/O.
- Small interfaces defined where they are used. `Store` lives in `internal/leaderboard`.
- Table-driven tests. Use `MemoryStore` or a fake store in unit tests.
- Integration tests talk to a real Redis; skip with `t.Skip` when `REDIS_ADDR` is unset.
  Each test uses its own board name (derived from `t.Name()`).
- HTTP server with `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout` set.
- The binary supports `-healthcheck`: GET `http://localhost:$PORT/healthz`, exit 0 or 1
  (the runtime image is distroless, no curl).

## Commands

```
go test -race ./...                         # all tests (integration tests need Redis)
REDIS_ADDR=localhost:6379 go test -race ./...
docker compose up --build                   # api + redis on :8080
golangci-lint run                           # if installed locally
```

## Phases (definition of done)

1. **v0.0** Skeleton, domain, MemoryStore, all GET/POST handlers on MemoryStore (sync
   write), httptest tests. Done: `go test -race ./...` passes; curl works locally.
2. **v0.1** RedisStore + integration tests, multi-stage Dockerfile
   (`golang:1.27` -> `gcr.io/distroless/static:nonroot`), docker-compose (redis with
   `--appendonly yes`, volume, healthcheck; api depends on healthy redis), GitHub
   Actions CI (gofmt check, vet, `go test -race` with a redis service container,
   golangci-lint, docker build), README v1. Done: fresh clone + `docker compose up`
   works, CI green, tag `v0.1.0`.
3. **v0.2** Ingest package, graceful shutdown, `/healthz` `/readyz`, API key middleware,
   validation, "around" endpoint, `docs/decisions.md`. Tests: flush on size, flush on
   interval, drain on close, 503 when full, 401 without key. Done: under load, Ctrl+C
   loses no accepted scores (proved by a test).
4. **Measure and scale** `cmd/loadgen` (N goroutines, duration, rate; results sent over a
   channel to one collector that prints p50/p95/p99, throughput, error rate). Compare
   sync vs async locally; put real results in README. Compose profile with nginx in
   front of `--scale api=3`. CI pushes the image to GHCR on `v*` tags.
5. **v1.0** Render deploy (Docker web service + Key Value), CI job on `main` that calls
   the Render deploy hook (secret `RENDER_DEPLOY_HOOK`) only after tests pass.
   Final README.

## README must contain

Description and CI badge; quick start (`docker compose up` + 3 curl examples); API
table; Mermaid architecture diagram; design decisions and their costs; how to run
tests; measured results with environment; known limitations; and a short
"How this was built" section stating honestly that the code was written with
Claude Code under my direction, and what I reviewed and tested myself.
