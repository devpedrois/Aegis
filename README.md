# Aegis
High-performance reverse proxy in pure Go with adaptive rate limiting (P95-based), circuit breaker, concurrent health checks, and a live terminal dashboard. Zero-trust architecture with 8 independent security layers, no frameworks, stdlib only.

## Security Note
The startup validator blocks loopback, link-local, private, metadata, and other reserved backend targets by default. Local development can opt into loopback-only backends with `development.allow_loopback_backends: true`, which is used by the checked-in `aegis.yml`.
