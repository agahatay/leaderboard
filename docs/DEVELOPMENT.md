# Development notes

How the code is organised and the conventions it follows. API, configuration,
Redis keys, tests and deploy are described in the [README](../README.md);
design trade-offs are in [decisions.md](decisions.md).

## Layout

```
cmd/server/                config, wiring, HTTP server, graceful shutdown, -healthcheck
cmd/loadgen/               load generator (N goroutines -> one collector)
internal/api/              handlers, middleware (API key, logging), JSON helpers
internal/leaderboard/      domain types, Service, Store interface, MemoryStore
internal/leaderboard/storetest/  shared Store test suite (MemoryStore and Redis)
internal/store/redisstore/ Store implementation on Redis
internal/ingest/           buffered channel + worker pool + batched writes
internal/config/           env parsing with defaults and validation
deploy/nginx/              nginx configs for the compose.lb.yml profile
docs/                      decision records and raw benchmark output
```

Dependencies are wired by hand in `cmd/server/main.go`. No web framework, ORM
or DI container; the router is the standard library `net/http` mux.

## Conventions

- Anything that does I/O takes `context.Context` as its first parameter.
  Every Redis call runs with a timeout (`REDIS_TIMEOUT`).
- Wrap errors with context (`fmt.Errorf("zadd %s: %w", key, err)`) and
  check them with `errors.Is`.
- Interfaces are small and defined where they are used: `Store` lives in
  `internal/leaderboard`, not next to its implementations.
- Tests are table-driven and use `MemoryStore` or a fake store.
- Redis integration tests are skipped when `REDIS_ADDR` is unset. Each test
  uses its own key prefix derived from `t.Name()` (`storetest.Prefix`).
- The HTTP server sets `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`
  and `IdleTimeout`.
- The runtime image is distroless (no shell, no curl), so the container
  healthcheck runs the binary itself with `-healthcheck`.
- Config comes from environment variables only and fails fast on invalid
  values. Real values go in `.env` (not committed); `.env.example` lists them.

## Before committing

```sh
gofmt -l .           # must print nothing
go vet ./...
go test -race ./...  # add REDIS_ADDR=localhost:6379 to include Redis tests
golangci-lint run
```

CI runs the same checks plus a Docker build.
