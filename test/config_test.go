package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cfgpkg "github.com/user/aegis/internal/config"
)

func TestLoadConfigValidYAML(t *testing.T) {
	t.Parallel()

	configPath := writeTempConfig(t, `
server:
  port: 8080
  read_timeout: 5s
  write_timeout: 10s
  idle_timeout: 120s
  max_header_bytes: 8192
  max_body_bytes: 10485760
  shutdown_timeout: 30s
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

	cfg, err := cfgpkg.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Fatalf("Server.Port = %d, want 8080", cfg.Server.Port)
	}

	if len(cfg.Backends) != 1 {
		t.Fatalf("len(Backends) = %d, want 1", len(cfg.Backends))
	}

	if cfg.Server.ReadTimeout != 5*time.Second {
		t.Fatalf("Server.ReadTimeout = %s, want 5s", cfg.Server.ReadTimeout)
	}
}

func TestLoadConfigRejectsZeroBackends(t *testing.T) {
	t.Parallel()

	configPath := writeTempConfig(t, `
server:
  port: 8080
backends: []
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

	_, err := cfgpkg.LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want backend validation error")
	}

	if !strings.Contains(err.Error(), "at least one backend") {
		t.Fatalf("LoadConfig() error = %v, want backend validation error", err)
	}
}

func TestLoadConfigRejectsInvalidPort(t *testing.T) {
	t.Parallel()

	configPath := writeTempConfig(t, `
server:
  port: 70000
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

	_, err := cfgpkg.LoadConfig(configPath)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want port validation error")
	}

	if !strings.Contains(err.Error(), "port") {
		t.Fatalf("LoadConfig() error = %v, want port validation error", err)
	}
}

func TestValidateRejectsLoopbackBackend(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Backends = []cfgpkg.BackendConfig{{URL: "http://127.0.0.1:8081", Weight: 1}}

	err := cfgpkg.Validate(cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want SSRF error")
	}

	if !strings.Contains(err.Error(), "private/reserved IP") {
		t.Fatalf("Validate() error = %v, want SSRF error", err)
	}
}

func TestValidateRejectsMetadataBackend(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Backends = []cfgpkg.BackendConfig{{URL: "http://169.254.169.254", Weight: 1}}

	err := cfgpkg.Validate(cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want metadata SSRF error")
	}

	if !strings.Contains(err.Error(), "private/reserved IP") {
		t.Fatalf("Validate() error = %v, want metadata SSRF error", err)
	}
}

func TestValidateRejectsUnspecifiedIPv4Backend(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Backends = []cfgpkg.BackendConfig{{URL: "http://0.0.0.0:8081", Weight: 1}}

	err := cfgpkg.Validate(cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want unspecified IPv4 SSRF error")
	}

	if !strings.Contains(err.Error(), "private/reserved IP") {
		t.Fatalf("Validate() error = %v, want unspecified IPv4 SSRF error", err)
	}
}

func TestValidateRejectsUniqueLocalIPv6Backend(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Backends = []cfgpkg.BackendConfig{{URL: "http://[fd00::1]:8081", Weight: 1}}

	err := cfgpkg.Validate(cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want unique local IPv6 SSRF error")
	}

	if !strings.Contains(err.Error(), "private/reserved IP") {
		t.Fatalf("Validate() error = %v, want unique local IPv6 SSRF error", err)
	}
}

func TestValidateRejectsBackendUserInfo(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Backends = []cfgpkg.BackendConfig{{URL: "http://user:pass@8.8.8.8:8081", Weight: 1}}

	err := cfgpkg.Validate(cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want userinfo rejection")
	}

	if !strings.Contains(err.Error(), "must not contain credentials") {
		t.Fatalf("Validate() error = %v, want userinfo rejection", err)
	}
}

func TestValidateAllowsLoopbackBackendWhenDevelopmentBypassEnabled(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Development.AllowLoopbackBackends = true
	cfg.Backends = []cfgpkg.BackendConfig{{URL: "http://127.0.0.1:8081", Weight: 1}}

	if err := cfgpkg.Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateStillRejectsMetadataBackendWhenDevelopmentBypassEnabled(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Development.AllowLoopbackBackends = true
	cfg.Backends = []cfgpkg.BackendConfig{{URL: "http://169.254.169.254", Weight: 1}}

	err := cfgpkg.Validate(cfg)
	if err == nil {
		t.Fatal("Validate() error = nil, want metadata SSRF error")
	}

	if !strings.Contains(err.Error(), "private/reserved IP") {
		t.Fatalf("Validate() error = %v, want metadata SSRF error", err)
	}
}

func TestValidateAcceptsPublicBackendIP(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Backends = []cfgpkg.BackendConfig{{URL: "http://8.8.8.8:8081", Weight: 1}}

	if err := cfgpkg.Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
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
