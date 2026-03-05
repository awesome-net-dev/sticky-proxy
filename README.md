# sticky-proxy

A high-performance sticky-session HTTP/WebSocket reverse proxy written in Go. Routes requests from the same user to the same backend server using JWT-based identification and a two-tier caching strategy (local + Redis).

## Features

- **Sticky sessions** — users are consistently routed to the same backend via Redis-backed mappings with local cache for fast repeated access
- **JWT authentication** — extracts `userId` from Bearer tokens with HMAC signature validation and token caching
- **WebSocket support** — full bidirectional proxying with sticky session persistence
- **Circuit breakers** — for both Redis and individual backends, with automatic CRC32 hash fallback when Redis is unavailable
- **Active health checking** — periodic backend probes with configurable intervals
- **Per-user rate limiting** — token bucket algorithm (100 tokens/sec, 200 burst) with automatic cleanup
- **Prometheus metrics** — request counters, latency histograms, cache hit rates, and more on `/metrics`
- **Graceful shutdown** — drains in-flight requests on SIGTERM/SIGINT (30s timeout)

## Use Cases

```mermaid
graph LR
    Client((Client))
    Ops((Ops / Admin))

    Client -->|HTTP request with JWT| SendRequest[Send Request]
    Client -->|WebSocket upgrade| OpenWS[Open WebSocket]
    Client -->|Excessive requests| HitRateLimit[Hit Rate Limit → 429]

    Ops -->|GET /healthz| CheckHealth[Check Health]
    Ops -->|GET /metrics| ScrapeMetrics[Scrape Prometheus Metrics]
    Ops -->|docker compose up| Deploy[Deploy Stack]

    SendRequest --> StickyRoute[Sticky-Routed to Backend]
    OpenWS --> StickyRoute
```

## Architecture

### Component Overview

```mermaid
graph TB
    subgraph Clients
        C1[Client 1]
        C2[Client 2]
        C3[Client N]
    end

    subgraph Proxy["Proxy :8080"]
        JWT[JWT Auth + Cache]
        RL[Rate Limiter]
        LC[Local Cache]
        RD[Redis Client + CB]
        HF[CRC32 Hash Fallback]
        BM[Backend Manager + CB]
        WS[WebSocket Relay]
        HC[Health Checker]
        MET[Metrics Collector]
    end

    subgraph Storage
        Redis[(DragonflyDB / Redis)]
    end

    subgraph Backends
        B1[Backend 1]
        B2[Backend 2]
        B3[Backend 3]
    end

    C1 & C2 & C3 --> JWT
    JWT --> RL
    RL --> LC
    LC -->|miss| RD
    RD -->|circuit open| HF
    RD <--> Redis
    LC & RD & HF --> BM
    BM --> B1 & B2 & B3
    BM -->|WebSocket| WS
    WS --> B1 & B2 & B3
    HC -->|probe /healthz| B1 & B2 & B3
    HC <-->|update backends:active| Redis
```

### HTTP Request Flow

```mermaid
sequenceDiagram
    participant C as Client
    participant P as Proxy
    participant JWT as JWT Auth
    participant RL as Rate Limiter
    participant LC as Local Cache
    participant R as Redis (Lua)
    participant HF as Hash Fallback
    participant B as Backend

    C->>P: HTTP request (Authorization: Bearer <token>)
    P->>JWT: Extract userId from token
    alt Invalid / missing JWT
        JWT-->>P: error
        P-->>C: 401 Unauthorized
    end
    JWT-->>P: userId

    P->>RL: Allow(userId)?
    alt Rate limit exceeded
        RL-->>P: denied
        P-->>C: 429 Too Many Requests
    end
    RL-->>P: allowed

    P->>LC: Get(userId)
    alt Local cache hit & backend available
        LC-->>P: backend URL
    else Cache miss or backend unavailable
        LC-->>P: miss
        P->>R: Lua script (sticky:userId, backends:active)
        alt Redis circuit breaker OPEN
            R-->>P: circuit open
            P->>HF: CRC32 hash over cached backends
            HF-->>P: backend URL
        else Redis OK
            R-->>P: backend URL
            P->>LC: Set(userId, backend)
        else Redis fails & no cached backends
            R-->>P: error
            P-->>C: 503 Service Unavailable
        end
    end

    P->>B: Forward request (reverse proxy)
    B-->>P: Response
    P-->>C: Response
```

### WebSocket Flow

```mermaid
sequenceDiagram
    participant C as Client
    participant P as Proxy
    participant B as Backend

    C->>P: HTTP Upgrade: websocket + JWT
    Note over P: Auth + sticky lookup<br/>(same as HTTP flow)
    P->>B: Dial ws:// backend
    B-->>P: 101 Switching Protocols
    P-->>C: 101 Switching Protocols

    par Bidirectional relay
        loop Client → Backend
            C->>P: WS message
            P->>B: WS message
        end
    and
        loop Backend → Client
            B->>P: WS message
            P->>C: WS message
        end
    end

    alt Either side closes
        C->>P: Close frame
        P->>B: Close frame
    end
```

### Redis Circuit Breaker States

```mermaid
stateDiagram-v2
    [*] --> Closed

    Closed --> Closed: Success → reset failures
    Closed --> Open: failures ≥ threshold (default 5)

    Open --> Open: cooldown not elapsed → hash fallback
    Open --> HalfOpen: cooldown elapsed (default 30s)

    HalfOpen --> Closed: Probe succeeds → reset
    HalfOpen --> Open: Probe fails → reopen
```

### Backend Circuit Breaker States

```mermaid
stateDiagram-v2
    [*] --> Healthy

    Healthy --> Healthy: Proxy success
    Healthy --> Evicted: failures ≥ threshold (default 3)

    Evicted --> Evicted: cooldown not elapsed → 503
    note right of Evicted: Sticky mappings invalidated<br/>in local cache + Redis
    Evicted --> Healthy: cooldown elapsed (default 1m) → auto-reset

```

### Health Checker Behavior

```mermaid
sequenceDiagram
    participant HC as Health Checker
    participant R as Redis
    participant B1 as Backend 1
    participant B2 as Backend 2

    loop Every 10s
        HC->>R: SMEMBERS backends:active
        R-->>HC: [backend1, backend2, ...]
        HC->>R: Refresh cached backend list (for hash fallback)

        par Bounded concurrency (max 10)
            HC->>B1: GET /healthz
            B1-->>HC: 200 OK
        and
            HC->>B2: GET /healthz
            B2-->>HC: timeout / error
        end

        alt 3 consecutive failures
            HC->>R: SREM backends:active backend2
            Note over HC: Mark backend2 unhealthy
        end

        alt Was unhealthy, 1 success
            HC->>R: SADD backends:active backend2
            Note over HC: Mark backend2 healthy
        end
    end
```

### Deployment Topology

```mermaid
graph TB
    subgraph Docker Compose
        subgraph proxy_svc["proxy (512MB, 1 CPU)"]
            SP[sticky-proxy :8080]
        end

        subgraph redis_svc["redis (1GB, 2 CPU)"]
            DF[DragonflyDB :6379]
        end

        subgraph backends["backends (256MB, 0.5 CPU each)"]
            B1[backend1 :5678]
            B2[backend2 :5678]
            B3[backend3 :5678]
        end
    end

    Internet((Internet)) -->|:8080| SP
    SP <--> DF
    SP --> B1 & B2 & B3
    B1 & B2 & B3 -->|self-register on startup| DF
```

## Quick Start

### Docker Compose

The included `docker-compose.yml` starts DragonflyDB (Redis-compatible), 3 test backends, and the proxy:

```bash
docker compose up -d
```

The proxy is available at `http://localhost:8080`.

### Build from Source

**Requirements:** Go 1.25+

```bash
make build          # outputs bin/proxy and bin/backend
```

Run with a Redis instance available:

```bash
export JWT_SECRET="your-secret-key"
export REDIS_ADDR="localhost:6379"
./bin/proxy
```

## Configuration

All settings are configured via environment variables. Only `JWT_SECRET` is required.

| Variable | Default | Description |
|---|---|---|
| `JWT_SECRET` | *required* | HMAC secret for JWT validation |
| `PROXY_PORT` | `:8080` | Proxy listen address |
| `REDIS_ADDR` | `localhost:6379` | Redis/DragonflyDB address |
| `CACHE_TTL` | `24h` | User-to-backend mapping TTL |
| `REDIS_POOL_SIZE` | `100` | Redis connection pool size |
| `REDIS_MIN_IDLE_CONNS` | `10` | Minimum idle Redis connections |
| `REDIS_CB_THRESHOLD` | `5` | Failures before Redis circuit breaker opens |
| `REDIS_CB_COOLDOWN` | `30s` | Redis circuit breaker cooldown period |
| `JWT_CACHE_MAX_SIZE` | `100000` | Maximum cached JWT tokens |
| `EVICTION_THRESHOLD` | `3` | Backend failures before eviction |
| `EVICTION_COOLDOWN` | `1m` | Backend circuit breaker cooldown |
| `BACKEND_HEALTH_INTERVAL` | `10s` | Health check probe frequency |
| `LOG_FORMAT` | `json` | Log format: `json` or `text` |

## Endpoints

| Path | Description |
|---|---|
| `/` | Proxy handler — routes to sticky backend |
| `/healthz` | Health check — returns Redis and backend status |
| `/metrics` | Prometheus metrics in text exposition format |

### Prometheus Metrics

| Metric | Type | Description |
|---|---|---|
| `stickyproxy_requests_total` | counter | Total requests received |
| `stickyproxy_backend_requests_total` | counter | Requests per backend (labeled) |
| `stickyproxy_backend_errors_total` | counter | Backend proxy errors |
| `stickyproxy_redis_failures_total` | counter | Redis operation failures |
| `stickyproxy_redis_cb_fallbacks_total` | counter | Hash fallbacks due to circuit breaker |
| `stickyproxy_cache_hits_total` | counter | Cache hits by layer (`local`, `redis`) |
| `stickyproxy_cache_misses_total` | counter | Cache misses (new assignments) |
| `stickyproxy_auth_failures_total` | counter | JWT authentication failures |
| `stickyproxy_websocket_connections_total` | counter | WebSocket connections opened |
| `stickyproxy_rate_limited_total` | counter | Requests rejected by rate limiter |
| `stickyproxy_active_connections` | gauge | Currently active connections |
| `stickyproxy_healthy_backends` | gauge | Number of healthy backends |
| `stickyproxy_request_duration_seconds` | histogram | Request latency distribution |

## Development

```bash
make test           # run tests with race detector
make lint           # run golangci-lint
make vet            # run go vet
make fmt            # format code
make fmt-check      # verify formatting
```

### Project Structure

```
cmd/
  proxy/            # main proxy server
  backend/          # test backend (self-registers in Redis)
internal/
  config/           # environment-based configuration
  proxy/            # core proxy logic
    proxy.go        # HTTP handler and routing
    backends.go     # backend manager with circuit breaker
    redis.go        # Redis client with circuit breaker
    user_cache.go   # local in-memory sticky cache
    jwt.go          # JWT token extraction
    jwt_cache.go    # JWT token caching
    health_checker.go  # active backend health probes
    rate_limiter.go # per-user token bucket
    websocket.go    # WebSocket bidirectional relay
    metrics.go      # Prometheus metrics
    sticky.lua      # Redis Lua script for atomic assignment
k6/                 # load testing utilities
```

### CI

GitHub Actions runs on push to `main` and on pull requests:
- Build, vet, format check, and tests (with `-race`)
- Linting via golangci-lint v2.4

## License

Apache License 2.0 — see [LICENSE](LICENSE).
