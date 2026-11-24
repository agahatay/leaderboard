# Design decisions

Short records of the choices that shape this service, and what they cost.
Newest last.

## 1. Best-score writes with `ZADD GT`

**Decision.** A score replaces the stored one only if it is higher.

**Why.** It is what most game leaderboards want, and it makes writes
idempotent: retrying a request, or replaying a batch after a partial
failure, cannot lower a score or double-count anything.

**Cost.** Boards cannot hold "latest" or cumulative scores without a
different command and different retry rules.

## 2. One behaviour suite for every `Store`

**Decision.** `internal/leaderboard/storetest` holds the store tests;
`MemoryStore` and the Redis store both run them.

**Why.** Handler and ingest tests use `MemoryStore`. That is only honest if
it behaves like Redis, including tie order (equal scores by player ID,
descending). The shared suite proves it.

**Cost.** Tests must use IDs derived from the test name and clean up their
Redis keys.

## 3. Every Redis call has its own timeout

**Decision.** Each store method wraps its context in `REDIS_TIMEOUT`
(default 200ms), and the client runs with `ContextTimeoutEnabled`.

**Why.** A slow Redis should turn into fast errors, not into requests and
goroutines piling up. Without `ContextTimeoutEnabled`, go-redis applies its
own 3s socket timeouts instead of our deadline.

**Cost.** Under heavy load some calls fail that would have succeeded with
more patience; clients must retry.

## 4. Asynchronous ingest by default

**Decision.** `POST /scores` validates, puts the event on a bounded channel
and answers 202. Worker goroutines write events in batches (one Redis
pipeline per batch), flushing at `BATCH_SIZE` events or every
`FLUSH_INTERVAL`. When the channel is full the handler answers 503 with
`Retry-After: 1` instead of waiting.

**Why.** One pipeline per batch needs far fewer round trips than one per
request. The bounded queue with a non-blocking send gives back-pressure:
overload becomes visible 503s, not unbounded memory or latency.

**Cost.** A 202 means "queued", not "stored":

- If the process **crashes** (OOM kill, SIGKILL, power loss), accepted
  scores still in the channel or in a worker's batch are lost.
- If Redis keeps failing, a batch is retried `MaxAttempts` (3) times with
  backoff (50ms, 100ms), then dropped and logged as an error.
- A score becomes visible in reads up to `FLUSH_INTERVAL` later.

`INGEST_MODE=sync` writes before answering (200) for callers that cannot
accept this.

## 5. Shutdown order and ownership of the channel

**Decision.** On SIGINT/SIGTERM: `http.Server.Shutdown` (stop accepting,
wait for in-flight requests), then `Ingester.Close` (close the channel,
workers drain and flush, wait for them). Both share `SHUTDOWN_TIMEOUT`.
Only the ingest package closes the channel.

**Why.** After `Shutdown` returns no handler can enqueue, so closing the
channel then cannot lose an accepted event. As a second guard, `Enqueue`
and `Close` share an `RWMutex`: if `Shutdown` times out with handlers still
running, a late `Enqueue` gets `ErrClosed` (503) instead of a panic from
sending on a closed channel.

**Proof.** `cmd/server/shutdown_test.go` runs the real `serve` function on
a TCP port, sends load from 16 clients, cancels the context mid-load and
checks that every score that got a 202 is in the store. With the
`ingester.Close` call removed, the same test reports lost scores.

**Cost.** Docker must allow more than `SHUTDOWN_TIMEOUT` before SIGKILL;
`docker-compose.yml` sets `stop_grace_period: 15s`.

## 6. API key compared over SHA-256 digests

**Decision.** Writes require `X-API-Key`. The header and the configured key
are both hashed with SHA-256 and compared with
`subtle.ConstantTimeCompare`. `API_KEY` is required at startup.

**Why.** Constant-time comparison avoids leaking the key through response
timing. Hashing first means both inputs are always 32 bytes, because
`ConstantTimeCompare` returns early when lengths differ, which would leak
the key's length.

**Cost.** One shared key: no per-client identity, revocation or rate limits.

## 7. "Around" is two Redis calls, not one atomic read

**Decision.** `ZREVRANK` finds the player's rank r, then
`ZRANGE ... REV` reads r-radius..r+radius.

**Why.** Simple and readable; a Lua script would make it atomic but adds
code that is harder to test and to keep in step with `MemoryStore`.

**Cost.** A write between the two calls can shift the window by a place,
so the player may appear one position off-centre. Acceptable for a
leaderboard view.

## 8. Liveness and readiness are separate

**Decision.** `/healthz` always answers 200 while the process runs.
`/readyz` answers 200 only if Redis answers `PING` within 500ms. The
process does not exit when Redis is down at startup.

**Why.** Restarting the API does not fix Redis. Orchestrators should stop
routing traffic (readiness), not kill the process (liveness).

**Cost.** While Redis is down, requests that reach the API fail with 500.

## 9. Several replicas behind nginx through a Compose override file

**Decision.** `compose.lb.yml` removes the API's host port (`ports: !reset []`)
and adds nginx on `:8080`, so `--scale api=3` works. The plain
`docker compose up` from the quick start is unchanged.

**Why.** Replicas cannot all publish host port 8080. A Compose profile can
add the nginx service but cannot take a port away from `api`, so a profile
alone would have needed a second copy of the `api` service.

**Cost.** A longer command (two `-f` files). nginx resolves `api` once at
startup, so it must be restarted after replicas are added or recreated.

## 10. Retrying a failed POST on another replica (`lb-retry.conf`)

**Decision.** `deploy/nginx/lb-retry.conf` adds
`proxy_next_upstream error timeout non_idempotent`, so nginx resends a POST
to another replica even when the first one may already have received it.
`lb.conf` keeps nginx's default, which never resends a POST once it was
sent. Pick one with `NGINX_CONF`.

**Why.** When a replica shuts down, nginx can send a request on a keep-alive
connection that the replica is closing. With the default config those
requests failed with 502 (14 in the measured run); with retries, 0. See
"Measured results" in the README.

**This is safe only because writes use `ZADD GT` (decision 1).** If the
first replica did process the request, the retry writes the same score
again, and "keep the higher score" makes the second write a no-op. In a
cumulative mode (`ZINCRBY`, "add to the total") the same retry would count
the score twice. Such a mode would need an idempotency key per request, or
this setting must be turned off.

**Cost.** A retried request can take longer, and a request the client sees
as failed (for example after its own timeout) may still have been written.
