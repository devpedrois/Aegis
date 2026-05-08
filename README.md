# 🛡️ Aegis

A high-performance reverse proxy written in pure Go — weighted load balancing, adaptive rate limiting, per-backend circuit breakers, and a live terminal dashboard. Zero-trust posture from startup validation through graceful shutdown.

![Go Version](https://img.shields.io/badge/go-1.22%2B-blue)
![License](https://img.shields.io/badge/license-MIT-green)
![Race Free](https://img.shields.io/badge/race--detector-clean-brightgreen)

---

## 📋 Table of Contents

- [About](#-about)
- [Architecture](#-architecture)
- [Stack](#-stack)
- [Security](#-security)
- [Getting Started](#-getting-started)
- [CLI](#-cli)
- [Configuration](#-configuration)
- [Development](#-development)
- [Project Structure](#-project-structure)
- [Architecture Decisions](#-architecture-decisions)
- [License](#-license)

---

## 🎯 About

**Aegis** is a production-grade reverse proxy demonstrating idiomatic Go patterns under real load:

- Weighted round-robin backend selection with warm-up on recovery
- Active health checks — one goroutine per backend with configurable thresholds
- Per-IP token bucket rate limiting extracted exclusively from `RemoteAddr`
- Adaptive control loop that adjusts rate limits based on observed P95 latency
- Per-backend circuit breaker with `closed → open → half-open` state machine
- Live terminal dashboard powered by Bubble Tea, updated every second
- Eight independent security layers — if one fails, the rest keep the system safe

Every security-sensitive decision is documented in code with `// [SECURITY]` comments.

---

## 🏗️ Architecture

### Request Flow

```mermaid
flowchart LR
    C[Client] --> SH[Security Headers]
    SH --> VR[Validation]
    VR --> RL[Rate Limiter]
    RL --> PX[Proxy Handler]
    PX --> BP[Backend Pool]
    BP --> B1[Backend 1]
    BP --> B2[Backend 2]
    BP --> B3[Backend 3]
```

### Adaptive Control Loop

```mermaid
flowchart TD
    T[Ticker — 10s] --> S[Collect Snapshot]
    S --> P[Compute P95 across healthy backends]
    P --> D{P95 > threshold?}
    D -- yes --> R[rate × 0.8 — clamp to min]
    D -- no --> G{P95 < threshold × 0.7?}
    G -- yes --> V[rate × 1.1 — clamp to max]
    G -- no --> N[No change]
    R --> H[Hysteresis flag — skip next reduction]
    V --> AP[Propagate new rate to all buckets]
    N --> AP
    H --> AP
```

### Circuit Breaker State Machine

```mermaid
stateDiagram-v2
    [*] --> Closed
    Closed --> Open : failure_threshold consecutive errors
    Open --> HalfOpen : open_timeout elapsed
    HalfOpen --> Closed : success_threshold probes succeed
    HalfOpen --> Open : any probe fails
```

---

## 🛠️ Stack

| Layer | Technology |
|---|---|
| Language | Go 1.22+ |
| HTTP proxy | `net/http/httputil.ReverseProxy` (stdlib) |
| Concurrency | `sync.Map`, `sync.RWMutex`, `atomic` (stdlib) |
| Configuration | `gopkg.in/yaml.v3` |
| Terminal UI | `github.com/charmbracelet/bubbletea` |
| TUI styling | `github.com/charmbracelet/lipgloss` |
| TUI components | `github.com/charmbracelet/bubbles` |
| Logging | `log/slog` (stdlib, Go 1.21+) |

Zero HTTP frameworks. Stdlib for everything except the TUI layer.

---

## 🔒 Security

Aegis applies defense in depth across eight independent layers. A failure in any one layer does not compromise the others.

### Layer 1 — Configuration Validation

Runs once at startup. Fatal errors abort the process before any port is opened.

- **Anti-SSRF:** resolves DNS for every backend URL and blocks loopback (`127.0.0.0/8`), link-local (`169.254.0.0/16`), private ranges (`10/8`, `172.16/12`, `192.168/16`), IPv6 loopback (`::1`), and the AWS metadata endpoint (`169.254.169.254`). `// [SECURITY] SSRF Prevention`
- Rejects schemes other than `http` or `https`
- Rejects URLs with userinfo (`http://user:pass@host`)
- Validates numeric ranges: port `1–65535`, all timeouts and thresholds positive, at least one backend configured

### Layer 2 — Request Validation

Applied per-request before any proxying occurs.

| Check | Rejection |
|---|---|
| Body exceeds `max_body_bytes` | `413` |
| Method not in allowlist | `405` |
| Both `Content-Length` and `Transfer-Encoding` present | `400` — smuggling prevention |
| Path contains `..` after `path.Clean()` | `400` — traversal prevention |
| `Host` header not in backend whitelist | `400` — injection prevention |

`// [SECURITY] Defense in Depth — request validation independent of rate limiter and circuit breaker`

### Layer 3 — Security Headers

Applied to every response regardless of backend behavior.

```
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
X-XSS-Protection: 0
Referrer-Policy: strict-origin-when-cross-origin
Permissions-Policy: camera=(), microphone=(), geolocation=()
Server: aegis
```

`X-Powered-By` is stripped from backend responses before forwarding to clients.

### Layer 4 — Hop-by-hop Header Stripping

Removed before forwarding to backends:

```
Connection  Keep-Alive  Proxy-Authenticate  Proxy-Authorization
TE  Trailers  Transfer-Encoding  Upgrade
```

Headers listed in the `Connection` field of the incoming request are also removed. `// [SECURITY] Hop-by-hop Header Stripping — prevents header smuggling`

### Layer 5 — Rate Limiting (per-IP, token bucket)

- IP extracted **exclusively** from `net.SplitHostPort(req.RemoteAddr)` — never from `X-Forwarded-For` or any other header. `// [SECURITY] IP Spoofing prevention`
- Excess requests return `429` with a JSON body and `Retry-After` header
- Inactive buckets are purged on a configurable interval to prevent memory exhaustion. `// [SECURITY] Memory exhaustion prevention`

### Layer 6 — Adaptive Rate Control

- Evaluates P95 latency across all healthy backends every `evaluation_interval`
- Reduces the global rate by `reduction_factor` when P95 exceeds the threshold
- Recovers by `recovery_factor` when P95 drops 30% below the threshold
- Hysteresis flag prevents back-to-back reductions, avoiding oscillation

### Layer 7 — Circuit Breaker (per-backend)

- Opens after `failure_threshold` consecutive real-request failures (HTTP 5xx or timeout)
- Blocked backends receive no traffic in the open state — load balancer skips them entirely
- Half-open probing allows `half_open_max_requests` test requests before deciding to close or re-open
- Independent of health checks: health checks monitor `/health`, circuit breaker monitors real traffic

### Layer 8 — Aggressive Timeouts

```
ReadTimeout:  5s   — Slowloris protection
WriteTimeout: 10s  — Slow read protection
IdleTimeout:  120s — Connection exhaustion protection
```

`ResponseHeaderTimeout` on the backend transport is also configurable. `// [SECURITY] Timeout Configuration — prevents resource exhaustion attacks`

### Safe Logging

- Client IPs are masked (last two octets zeroed) before any log entry
- Request bodies, response bodies, cookies, tokens, and `Authorization` headers are never logged
- Backend errors are logged internally with full detail; the client receives only `{"error":"bad gateway"}` with status `502`

### Graceful Shutdown

- Captures `SIGINT` / `SIGTERM`
- Stops accepting new connections immediately
- Drains in-flight requests up to `shutdown_timeout`
- Cancels health check goroutines, rate limiter cleanup, metrics collector, and adaptive control loop via context propagation
- Logs `INFO Aegis shutdown complete` on clean exit

---

## 🚀 Getting Started

### 1. Clone the repository

```bash
git clone https://github.com/devpedrois/aegis.git
cd aegis
```

### 2. Start fake backends

```bash
go run scripts/fake_backend.go
```

Three servers start on ports `8081`, `8082`, and `8083`, each responding `200 OK` with their own name.

### 3. Run the proxy

```bash
make run
```

The proxy listens on `8080` and distributes requests across the three backends. The TUI dashboard launches automatically.

### 4. Send a request

```bash
curl -s http://localhost:8080/
```

### 5. Run in headless mode

```bash
make run-headless
```

Structured JSON logs to stdout. No TUI.

---

## 💻 CLI

```text
aegis [flags]

Flags:
  -c, --config string    Path to config file (default "aegis.yml")
      --headless         Run without TUI (log only)
      --log-level string Override log level (debug|info|warn|error)
  -h, --help             Show help
  -v, --version          Show version
```

---

## ⚙️ Configuration

The repository ships with a working `aegis.yml`:

```yaml
server:
  port: 8080
  read_timeout: 5s
  write_timeout: 10s
  idle_timeout: 120s
  max_header_bytes: 8192
  max_body_bytes: 10485760
  shutdown_timeout: 30s

backends:
  - url: "http://localhost:8081"
    weight: 1
  - url: "http://localhost:8082"
    weight: 1
  - url: "http://localhost:8083"
    weight: 2

health_check:
  interval: 10s
  timeout: 3s
  path: "/health"
  unhealthy_threshold: 3
  healthy_threshold: 2

rate_limit:
  requests_per_second: 100
  burst: 150
  cleanup_interval: 60s

adaptive:
  evaluation_interval: 10s
  latency_threshold_ms: 500
  reduction_factor: 0.8
  recovery_factor: 1.1
  min_rate: 10
  max_rate: 500

circuit_breaker:
  failure_threshold: 5
  success_threshold: 3
  open_timeout: 30s
  half_open_max_requests: 3

logging:
  level: "info"
  format: "json"

tui:
  refresh_interval: 1s
  enabled: true

development:
  allow_loopback_backends: true
```

### Field Reference

#### `server`

| Field | Description | Default |
|---|---|---|
| `port` | TCP port for the public listener | `8080` |
| `read_timeout` | Max time to read a client request | `5s` |
| `write_timeout` | Max time to write a response | `10s` |
| `idle_timeout` | Max keep-alive idle time | `120s` |
| `max_header_bytes` | Max accepted header size in bytes | `8192` |
| `max_body_bytes` | Max accepted body size in bytes | `10485760` |
| `shutdown_timeout` | Grace period for in-flight requests on shutdown | `30s` |

#### `backends`

| Field | Description | Required |
|---|---|---|
| `url` | Upstream backend URL | yes |
| `weight` | Relative traffic weight in the round-robin schedule | yes |

#### `health_check`

| Field | Description | Default |
|---|---|---|
| `interval` | Delay between active health probes | `10s` |
| `timeout` | Timeout per health probe | `3s` |
| `path` | Path used for active checks | `/health` |
| `unhealthy_threshold` | Consecutive failures to mark a backend unhealthy | `3` |
| `healthy_threshold` | Consecutive successes to re-enable a backend | `2` |

#### `rate_limit`

| Field | Description | Default |
|---|---|---|
| `requests_per_second` | Token refill rate per client IP | `100` |
| `burst` | Max bucket capacity per client IP | `150` |
| `cleanup_interval` | Frequency of stale bucket cleanup | `60s` |

#### `adaptive`

| Field | Description | Default |
|---|---|---|
| `evaluation_interval` | How often the control loop evaluates P95 | `10s` |
| `latency_threshold_ms` | P95 threshold that triggers a rate reduction | `500` |
| `reduction_factor` | Multiplier applied when P95 is too high | `0.8` |
| `recovery_factor` | Multiplier applied when P95 normalizes | `1.1` |
| `min_rate` | Lower bound for adaptive rate | `10` |
| `max_rate` | Upper bound for adaptive rate | `500` |

#### `circuit_breaker`

| Field | Description | Default |
|---|---|---|
| `failure_threshold` | Consecutive failures to open a breaker | `5` |
| `success_threshold` | Half-open successes required to close | `3` |
| `open_timeout` | Time in open state before half-open probing | `30s` |
| `half_open_max_requests` | Max concurrent probe requests in half-open | `3` |

#### `logging`

| Field | Description | Default |
|---|---|---|
| `level` | Log level: `debug`, `info`, `warn`, `error` | `info` |
| `format` | Output format: `json` or `text` | `json` |

#### `tui`

| Field | Description | Default |
|---|---|---|
| `refresh_interval` | Dashboard refresh cadence | `1s` |
| `enabled` | Enable the Bubble Tea dashboard | `true` |

#### `development`

| Field | Description | Default |
|---|---|---|
| `allow_loopback_backends` | Bypass SSRF check for loopback backends (local testing only) | `false` |

---

## 🧪 Development

```bash
make build        # Build binary to bin/aegis
make run          # Run with TUI dashboard
make run-headless # Run with log-only output
make test         # go test -v ./...
make race         # go test -race -count=1 ./...
make lint         # go vet ./...
```

Run the stress test suite with the race detector:

```bash
go test -race -tags=stress ./test/
```

---

## 📁 Project Structure

```
aegis/
├── cmd/
│   └── aegis/
│       └── main.go                # Entry point — loads config, starts proxy or TUI
├── internal/
│   ├── config/
│   │   ├── config.go              # Config structs and YAML parser
│   │   └── validate.go            # Startup validation — ranges, SSRF, backend URLs
│   ├── proxy/
│   │   ├── proxy.go               # HTTP handler — dispatches to backend pool
│   │   ├── director.go            # Director func — rewrites URL, strips hop-by-hop, sets X-Request-ID
│   │   ├── transport.go           # Custom RoundTripper — latency measurement, error recording
│   │   └── middleware.go          # Middleware chain — security headers, logging, recovery
│   ├── pool/
│   │   ├── pool.go                # BackendPool — thread-safe, weighted round-robin
│   │   ├── backend.go             # Backend struct with atomic state fields
│   │   └── healthcheck.go         # Per-backend health check goroutines with warm-up logic
│   ├── ratelimit/
│   │   ├── bucket.go              # TokenBucket — TryConsume, Refill, SetRate
│   │   ├── limiter.go             # RateLimiter middleware — sync.Map of IP → bucket
│   │   ├── adaptive.go            # Adaptive control loop — P95 evaluation and rate propagation
│   │   └── cleanup.go             # Stale bucket GC goroutine
│   ├── circuit/
│   │   ├── breaker.go             # CircuitBreaker — AllowRequest, RecordSuccess, RecordFailure
│   │   └── state.go               # State enum — Closed, Open, HalfOpen + transitions
│   ├── metrics/
│   │   ├── collector.go           # Latency sample collector — sliding window per backend
│   │   ├── percentile.go          # P50 / P95 / P99 calculation
│   │   └── snapshot.go            # MetricsSnapshot — immutable read for TUI and adaptive loop
│   ├── security/
│   │   ├── headers.go             # Security header middleware
│   │   ├── validation.go          # Request validation middleware
│   │   ├── ipcheck.go             # ExtractIP from RemoteAddr only
│   │   └── ssrf.go                # IsPrivateIP + ValidateBackendURL
│   ├── tui/
│   │   ├── app.go                 # Bubble Tea Model — Init / Update / View
│   │   ├── views.go               # Header, backends table, rate limiter panel, event log
│   │   └── styles.go              # Lipgloss style constants
│   └── logging/
│       └── logger.go              # slog setup, IP masking, recent event ring buffer
├── test/
│   ├── proxy_test.go              # End-to-end proxy integration tests
│   ├── pool_test.go               # Backend pool and health check tests
│   ├── ratelimit_test.go          # Token bucket and adaptive tests
│   ├── circuit_test.go            # Circuit breaker state transition tests
│   ├── security_test.go           # Adversarial security tests
│   ├── helpers_test.go            # Fake backends with configurable latency and error rate
│   └── testdata/
│       ├── valid_config.yml
│       └── invalid_config.yml
├── scripts/
│   └── fake_backend.go            # Three fake HTTP backends for local testing
├── aegis.yml                      # Example configuration
├── Makefile
└── go.mod
```

---

## 🧠 Architecture Decisions

**`httputil.ReverseProxy` as the base** — The stdlib proxy handles HTTP/1.1 and HTTP/2 transparently, manages connection pooling via `http.Transport`, and exposes `Director` and `ModifyResponse` hooks for customization. Building on it avoids reimplementing protocol details while keeping full control over request rewriting and response transformation.

**Weighted round-robin with atomic counters** — Backend selection uses an atomic index over an expanded slice where each backend appears `weight` times. No locks on the hot path. Unhealthy and open-circuit backends are filtered before selection, not removed from the slice, so recovery requires no structural mutation.

**Warm-up on recovery** — A backend returning from unhealthy does not immediately receive its full share of traffic. Weight scales from 25% to 50% to 75% to 100% across consecutive successful health checks. This prevents a recovering backend from being overwhelmed before it stabilizes.

**Sliding window percentiles** — Latency samples are stored in a ring buffer capped at 1000 entries or 60 seconds, whichever is smaller. P95 is computed by sorting the window copy. Accuracy is sufficient for the adaptive control loop without the complexity of streaming estimators like t-digest.

**Hysteresis in the adaptive loop** — A flag prevents two consecutive reductions in the same evaluation cycle. After a reduction, the loop waits at least one full interval before reducing again, even if P95 remains high. This prevents a feedback spiral when the system is under transient load.

**`sync.Map` for IP buckets** — The bucket map is read far more than it is written. `sync.Map` amortizes the cost of concurrent reads with minimal contention. Each bucket uses its own `sync.Mutex` for the token accounting, keeping lock scope minimal.

**Circuit breaker independent of health checks** — Health checks probe a synthetic `/health` endpoint on a timer. The circuit breaker observes real traffic. A backend can pass health checks while its circuit is open (e.g., the health endpoint returns 200 but application endpoints return 500). Both layers must agree before traffic resumes.

**Context propagation throughout** — Every goroutine started by Aegis receives a `context.Context` derived from the root context. Canceling the root context on shutdown propagates to health check tickers, the rate limiter cleanup loop, the adaptive control loop, and the metrics collector. No goroutine leaks on clean shutdown.

---

## 📄 License

This project is licensed under the [MIT License](LICENSE).
