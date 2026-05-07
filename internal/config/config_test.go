package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	configPath := writeTempConfig(t, `
server:
  port: 8080
  unknown_field: true
backends:
  - url: "http://8.8.8.8:8081"
    weight: 1
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
`)

	_, err := LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want unknown field error")
	}

	if !strings.Contains(err.Error(), "field unknown_field not found") {
		t.Fatalf("LoadConfig() error = %v, want unknown field error", err)
	}
}

func TestValidatePinsResolvedBackendAddress(t *testing.T) {
	t.Parallel()

	originalLookupHost := lookupHost
	lookupHost = func(host string) ([]string, error) {
		return []string{"203.0.113.20"}, nil
	}
	t.Cleanup(func() {
		lookupHost = originalLookupHost
	})

	cfg := NewDefaultConfig()
	cfg.Backends = []BackendConfig{
		{URL: "http://proxy.example.com:8081", Weight: 1},
	}

	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	backend := cfg.Backends[0]
	if backend.PinnedAddress != "203.0.113.20:8081" {
		t.Fatalf("PinnedAddress = %q, want %q", backend.PinnedAddress, "203.0.113.20:8081")
	}

	if backend.ServerName != "proxy.example.com" {
		t.Fatalf("ServerName = %q, want %q", backend.ServerName, "proxy.example.com")
	}

	if backend.OriginalHost != "proxy.example.com:8081" {
		t.Fatalf("OriginalHost = %q, want %q", backend.OriginalHost, "proxy.example.com:8081")
	}
}

func TestLoadConfigAllowsLoopbackBackendsWhenDevelopmentBypassEnabled(t *testing.T) {
	t.Parallel()

	configPath := writeTempConfig(t, `
server:
  port: 8080
backends:
  - url: "http://localhost:8081"
    weight: 1
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
`)

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if !cfg.Development.AllowLoopbackBackends {
		t.Fatal("AllowLoopbackBackends = false, want true")
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "aegis.yml")

	if err := os.WriteFile(configPath, []byte(strings.TrimSpace(content)), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	return configPath
}
