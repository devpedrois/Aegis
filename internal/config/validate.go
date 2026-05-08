package config

import (
	"fmt"
	"net"

	securitypkg "github.com/user/aegis/internal/security"
)

type hostLookupFunc func(string) ([]string, error)

func Validate(cfg *Config) error {
	return validateWithHostLookup(cfg, net.LookupHost)
}

func validateWithHostLookup(cfg *Config, lookupHost hostLookupFunc) error {
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

		if err := validateBackendURL(cfg, &cfg.Backends[i], lookupHost); err != nil {
			return fmt.Errorf("backend %q failed validation: %w", cfg.Backends[i].URL, err)
		}
	}

	return nil
}

func validateBackendURL(cfg *Config, backend *BackendConfig, lookupHost hostLookupFunc) error {
	resolved, err := securitypkg.ValidateBackendURL(backend.URL, securitypkg.HostLookupFunc(lookupHost), cfg != nil && cfg.Development.AllowLoopbackBackends)
	if err != nil {
		return err
	}

	backend.OriginalHost = resolved.OriginalHost
	backend.ServerName = resolved.ServerName
	backend.PinnedAddress = resolved.PinnedAddress
	return nil
}

func allowLoopbackBackend(cfg *Config, ip net.IP) bool {
	if cfg == nil || !cfg.Development.AllowLoopbackBackends {
		return false
	}

	// [SECURITY] The development bypass is limited to loopback only and does not allow broader private networks.
	return ip.IsLoopback()
}
