# Aegis

Aegis is a high-performance reverse proxy written in pure Go. It balances traffic across multiple upstream backends, applies per-client rate limiting, observes latency in real time, and isolates degraded upstreams with an internal circuit breaker. The project follows a zero-trust posture from startup validation through request handling and shutdown.

The runtime is intentionally small and explicit. Aegis uses the Go standard library for HTTP serving and proxying, YAML for configuration, and Bubble Tea for the optional terminal dashboard. Every security-sensitive decision is documented in code with `// [SECURITY]` comments.

## Architecture Diagram

```text
Client → [Security Headers] → [Validation] → [Rate Limiter] → [Proxy]
                                                                  ↓
                                                         [Backend Pool]
                                                      ↙      ↓      ↘
                                                  Backend1 Backend2 Backend3
                                                      ↑
                                               [Health Checks] [Circuit Breaker]
                                               [Metrics Collector] → [Adaptive Logic]
                                                                       ↓
                                                               [Adjust Rate Limit]
```

## Features

- Reverse proxy based on `httputil.ReverseProxy`
- Weighted round-robin backend selection
- Active health checks with recovery warm-up
- Per-IP token bucket rate limiting
- Adaptive rate limiting driven by observed P95 latency
- Per-backend circuit breaker with half-open probing
- Live terminal dashboard powered by Bubble Tea
- Eight independent security layers with zero-trust defaults

## Installation

### Option 1: Build locally

```bash
make build
```

The binary will be generated at `bin/aegis`.

### Option 2: Install with Go

```bash
go install github.com/user/aegis/cmd/aegis@latest
```

## Usage

Run with the checked-in example configuration:

```bash
make run
```

Run in headless mode:

```bash
go run ./cmd/aegis -c aegis.yml --headless
```

Override the log level:

```bash
go run ./cmd/aegis -c aegis.yml --log-level debug
```

Show help:

```bash
go run ./cmd/aegis --help
```

Show version:

```bash
go run ./cmd/aegis --version
```

## CLI

```text
aegis [flags]

Flags:
  -c, --config string    Path to config file
      --headless         Run without TUI
      --log-level string Override log level
  -h, --help             Show help
  -v, --version          Show version
```

## Configuration

The project ships with `aegis.yml`:

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

- `port`
  - TCP port used by the public proxy listener.
  - Default: `8080`
- `allowed_hosts`
  - Optional public host whitelist enforced by request validation.
  - Default: empty
- `read_timeout`
  - Maximum time to read a client request.
  - Default: `5s`
- `write_timeout`
  - Maximum time to write a response to the client.
  - Default: `10s`
- `idle_timeout`
  - Maximum keep-alive idle time.
  - Default: `120s`
- `max_header_bytes`
  - Maximum accepted header size in bytes.
  - Default: `8192`
- `max_body_bytes`
  - Maximum accepted request body size in bytes.
  - Default: `10485760`
- `shutdown_timeout`
  - Grace period for graceful shutdown.
  - Default: `30s`

#### `backends`

- `url`
  - Upstream backend URL.
  - Required
- `weight`
  - Relative traffic weight in the round-robin schedule.
  - Default expectation: positive integer

#### `health_check`

- `interval`
  - Delay between active health probes.
  - Default: `10s`
- `timeout`
  - Timeout for each health probe.
  - Default: `3s`
- `path`
  - Path used for active health checks.
  - Default: `/health`
- `unhealthy_threshold`
  - Consecutive failures required to mark a backend unhealthy.
  - Default: `3`
- `healthy_threshold`
  - Consecutive successes required to re-enable a backend.
  - Default: `2`

#### `rate_limit`

- `requests_per_second`
  - Initial token refill rate per client IP.
  - Default: `100`
- `burst`
  - Maximum bucket capacity per client IP.
  - Default: `150`
- `cleanup_interval`
  - Frequency of stale bucket cleanup.
  - Default: `60s`

#### `adaptive`

- `evaluation_interval`
  - Frequency of adaptive rate evaluation.
  - Default: `10s`
- `latency_threshold_ms`
  - P95 latency threshold that triggers reduction.
  - Default: `500`
- `reduction_factor`
  - Multiplicative factor applied when latency is too high.
  - Default: `0.8`
- `recovery_factor`
  - Multiplicative factor applied when latency recovers.
  - Default: `1.1`
- `min_rate`
  - Lower bound for adaptive rate limiting.
  - Default: `10`
- `max_rate`
  - Upper bound for adaptive rate limiting.
  - Default: `500`

#### `circuit_breaker`

- `failure_threshold`
  - Consecutive real request failures required to open a breaker.
  - Default: `5`
- `success_threshold`
  - Successful half-open probes required to close a breaker.
  - Default: `3`
- `open_timeout`
  - Time spent in the open state before half-open probing.
  - Default: `30s`
- `half_open_max_requests`
  - Maximum concurrent probe budget in half-open state.
  - Default: `3`

#### `logging`

- `level`
  - Log level for the structured logger.
  - Default: `info`
- `format`
  - Output format. Supported values: `json`, `text`.
  - Default: `json`

#### `tui`

- `refresh_interval`
  - Dashboard refresh cadence.
  - Default: `1s`
- `enabled`
  - Enables the Bubble Tea dashboard.
  - Default: `true`

#### `development`

- `allow_loopback_backends`
  - Development-only bypass that allows loopback upstreams after validation.
  - Default: `false`
  - The checked-in example sets this to `true` for local testing.

## Security

Aegis uses eight independent layers:

1. Configuration validation
   - Validates numeric ranges and rejects invalid or empty backend sets.
   - Resolves backend hosts and blocks loopback, private, link-local, metadata, and other reserved targets by default.

2. Request validation
   - Enforces body limits, method allowlists, host validation, path normalization, and request smuggling rejection.

3. Security headers
   - Applies `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, and a fixed `Server: aegis`.

4. Rate limiting
   - Uses per-IP token buckets keyed from `RemoteAddr` only.
   - Rejects excess load with `429` and `Retry-After`.

5. Circuit breaker
   - Opens after repeated upstream failures.
   - Prevents traffic from cascading into degraded backends.

6. Aggressive timeouts
   - Constrains read, write, idle, and upstream response windows to limit resource exhaustion.

7. Safe logging
   - Masks client IPs.
   - Avoids request bodies, response bodies, cookies, tokens, and sensitive headers.

8. Graceful shutdown
   - Stops accepting new work, cancels background goroutines, and drains active requests within the configured timeout.

## Architecture

### `cmd/aegis`

- Parses flags
- Loads YAML configuration
- Configures logging
- Starts the proxy runtime
- Handles graceful shutdown

### `internal/config`

- Defines config structures
- Loads YAML with known-field validation
- Validates startup constraints and backend SSRF rules

### `internal/proxy`

- Owns the reverse proxy handler
- Rewrites upstream requests
- Applies recovery and request logging
- Measures upstream latency in the custom transport

### `internal/pool`

- Stores backend membership
- Performs weighted round-robin selection
- Runs active health checks
- Tracks recovery warm-up state

### `internal/ratelimit`

- Implements token buckets
- Tracks per-IP state
- Cleans up idle buckets
- Applies adaptive global rate changes

### `internal/circuit`

- Manages per-backend breaker state
- Supports `closed`, `open`, and `half-open`

### `internal/metrics`

- Stores latency samples in sliding windows
- Computes P50, P95, and P99
- Exposes immutable snapshots for control and display

### `internal/security`

- Validates public requests
- Applies security headers
- Extracts trusted client IPs
- Validates upstream targets against SSRF patterns

### `internal/tui`

- Renders the live dashboard
- Displays backend status, latency, rate state, and recent events

### `internal/logging`

- Configures `slog`
- Stores recent events for the TUI
- Masks client IPs for operational safety

## Development

Useful commands:

```bash
make build
make test
make race
make lint
go test -race -tags=stress ./test/
```

## License

MIT
