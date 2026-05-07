package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

var lookupHost = net.LookupHost

func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config must not be nil")
	}

	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}

	if len(cfg.Backends) < 1 {
		return fmt.Errorf("at least one backend must be configured")
	}

	if cfg.Server.ReadTimeout <= 0 || cfg.Server.WriteTimeout <= 0 || cfg.Server.IdleTimeout <= 0 || cfg.Server.ShutdownTimeout <= 0 {
		return fmt.Errorf("server timeouts must be greater than zero")
	}

	if cfg.HealthCheck.Interval <= 0 || cfg.HealthCheck.Timeout <= 0 {
		return fmt.Errorf("health check timeouts must be greater than zero")
	}

	if cfg.HealthCheck.UnhealthyThreshold <= 0 || cfg.HealthCheck.HealthyThreshold <= 0 {
		return fmt.Errorf("health check thresholds must be greater than zero")
	}

	if cfg.RateLimit.RequestsPerSecond <= 0 || cfg.RateLimit.Burst <= 0 || cfg.RateLimit.CleanupInterval <= 0 {
		return fmt.Errorf("rate limit values must be greater than zero")
	}

	if cfg.Adaptive.EvaluationInterval <= 0 || cfg.Adaptive.LatencyThresholdMS <= 0 {
		return fmt.Errorf("adaptive settings must be greater than zero")
	}

	if cfg.Adaptive.ReductionFactor <= 0 || cfg.Adaptive.RecoveryFactor <= 0 || cfg.Adaptive.MinRate <= 0 || cfg.Adaptive.MaxRate <= 0 {
		return fmt.Errorf("adaptive rates must be greater than zero")
	}

	if cfg.Adaptive.MinRate >= cfg.Adaptive.MaxRate {
		return fmt.Errorf("min_rate must be less than max_rate")
	}

	if cfg.CircuitBreaker.FailureThreshold <= 0 || cfg.CircuitBreaker.SuccessThreshold <= 0 || cfg.CircuitBreaker.OpenTimeout <= 0 || cfg.CircuitBreaker.HalfOpenMaxRequests <= 0 {
		return fmt.Errorf("circuit breaker thresholds must be greater than zero")
	}

	for i := range cfg.Backends {
		if cfg.Backends[i].Weight <= 0 {
			return fmt.Errorf("backend weight must be greater than zero")
		}

		if err := validateBackendURL(cfg, &cfg.Backends[i]); err != nil {
			return fmt.Errorf("backend %q failed validation: %w", cfg.Backends[i].URL, err)
		}
	}

	return nil
}

func validateBackendURL(cfg *Config, backend *BackendConfig) error {
	parsedURL, err := url.Parse(backend.URL)
	if err != nil {
		return fmt.Errorf("parse backend URL: %w", err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("unsupported scheme")
	}

	if parsedURL.User != nil {
		return fmt.Errorf("backend URL must not contain credentials")
	}

	hostname := parsedURL.Hostname()
	if hostname == "" {
		return fmt.Errorf("backend URL must include a host")
	}

	port := parsedURL.Port()
	if port == "" {
		port = defaultPortForScheme(parsedURL.Scheme)
	}

	backend.OriginalHost = parsedURL.Host
	backend.ServerName = hostname

	if parsedIP := net.ParseIP(hostname); parsedIP != nil {
		// [SECURITY] Direct IP backends must be blocked when they target private or reserved ranges.
		if IsPrivateIP(parsedIP) {
			if allowLoopbackBackend(cfg, parsedIP) {
				backend.PinnedAddress = net.JoinHostPort(parsedIP.String(), port)
				return nil
			}

			return fmt.Errorf("backend URL resolves to private/reserved IP: %s", parsedIP.String())
		}

		backend.PinnedAddress = net.JoinHostPort(parsedIP.String(), port)
		return nil
	}

	addresses, err := lookupHost(hostname)
	if err != nil {
		return fmt.Errorf("resolve backend host %q: %w", hostname, err)
	}

	var pinnedIP string
	for _, address := range addresses {
		ip := net.ParseIP(address)
		if ip == nil {
			return fmt.Errorf("resolved backend host %q to invalid IP %q", hostname, address)
		}

		// [SECURITY] DNS resolution is validated to block SSRF through hostnames that map to private services.
		if IsPrivateIP(ip) {
			if allowLoopbackBackend(cfg, ip) {
				if pinnedIP == "" {
					// [SECURITY] Development loopback bypass is explicit and still pins the validated local address.
					pinnedIP = ip.String()
				}

				continue
			}

			return fmt.Errorf("backend URL resolves to private/reserved IP: %s", ip.String())
		}

		if pinnedIP == "" {
			// [SECURITY] The upstream dial target is pinned at startup to reduce DNS rebinding risk after validation.
			pinnedIP = ip.String()
		}
	}

	if pinnedIP == "" {
		return fmt.Errorf("resolved backend host %q to no routable IPs", hostname)
	}

	backend.PinnedAddress = net.JoinHostPort(pinnedIP, port)
	return nil
}

func allowLoopbackBackend(cfg *Config, ip net.IP) bool {
	if cfg == nil || !cfg.Development.AllowLoopbackBackends {
		return false
	}

	// [SECURITY] The development bypass is limited to loopback only and does not allow broader private networks.
	return ip.IsLoopback()
}

func defaultPortForScheme(scheme string) string {
	if scheme == "https" {
		return "443"
	}

	return "80"
}

func IsPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}

	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 169 && ip4[1] == 254:
			// [SECURITY] Link-local and metadata ranges are blocked to prevent cloud credential SSRF.
			return true
		}
	}

	if strings.EqualFold(ip.String(), "::1") {
		return true
	}

	return false
}
