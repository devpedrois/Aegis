package config

import (
	"bytes"
	"fmt"
	"os"
	"time"
)

import "gopkg.in/yaml.v3"

type Config struct {
	Server         ServerConfig         `yaml:"server"`
	Backends       []BackendConfig      `yaml:"backends"`
	HealthCheck    HealthCheckConfig    `yaml:"health_check"`
	RateLimit      RateLimitConfig      `yaml:"rate_limit"`
	Adaptive       AdaptiveConfig       `yaml:"adaptive"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Logging        LoggingConfig        `yaml:"logging"`
	TUI            TUIConfig            `yaml:"tui"`
	Development    DevelopmentConfig    `yaml:"development"`
}

type ServerConfig struct {
	Port            int           `yaml:"port"`
	AllowedHosts    []string      `yaml:"allowed_hosts"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	IdleTimeout     time.Duration `yaml:"idle_timeout"`
	MaxHeaderBytes  int           `yaml:"max_header_bytes"`
	MaxBodyBytes    int64         `yaml:"max_body_bytes"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type BackendConfig struct {
	URL           string `yaml:"url"`
	Weight        int    `yaml:"weight"`
	PinnedAddress string `yaml:"-"`
	ServerName    string `yaml:"-"`
	OriginalHost  string `yaml:"-"`
}

type HealthCheckConfig struct {
	Interval           time.Duration `yaml:"interval"`
	Timeout            time.Duration `yaml:"timeout"`
	Path               string        `yaml:"path"`
	UnhealthyThreshold int           `yaml:"unhealthy_threshold"`
	HealthyThreshold   int           `yaml:"healthy_threshold"`
}

type RateLimitConfig struct {
	RequestsPerSecond float64       `yaml:"requests_per_second"`
	Burst             int           `yaml:"burst"`
	CleanupInterval   time.Duration `yaml:"cleanup_interval"`
}

type AdaptiveConfig struct {
	EvaluationInterval time.Duration `yaml:"evaluation_interval"`
	LatencyThresholdMS int           `yaml:"latency_threshold_ms"`
	ReductionFactor    float64       `yaml:"reduction_factor"`
	RecoveryFactor     float64       `yaml:"recovery_factor"`
	MinRate            float64       `yaml:"min_rate"`
	MaxRate            float64       `yaml:"max_rate"`
}

type CircuitBreakerConfig struct {
	FailureThreshold    int           `yaml:"failure_threshold"`
	SuccessThreshold    int           `yaml:"success_threshold"`
	OpenTimeout         time.Duration `yaml:"open_timeout"`
	HalfOpenMaxRequests int           `yaml:"half_open_max_requests"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type TUIConfig struct {
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	Enabled         bool          `yaml:"enabled"`
}

type DevelopmentConfig struct {
	AllowLoopbackBackends bool `yaml:"allow_loopback_backends"`
}

func NewDefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Port:            8080,
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    10 * time.Second,
			IdleTimeout:     120 * time.Second,
			MaxHeaderBytes:  8192,
			MaxBodyBytes:    10 * 1024 * 1024,
			ShutdownTimeout: 30 * time.Second,
		},
		HealthCheck: HealthCheckConfig{
			Interval:           10 * time.Second,
			Timeout:            3 * time.Second,
			Path:               "/health",
			UnhealthyThreshold: 3,
			HealthyThreshold:   2,
		},
		RateLimit: RateLimitConfig{
			RequestsPerSecond: 100,
			Burst:             150,
			CleanupInterval:   60 * time.Second,
		},
		Adaptive: AdaptiveConfig{
			EvaluationInterval: 10 * time.Second,
			LatencyThresholdMS: 500,
			ReductionFactor:    0.8,
			RecoveryFactor:     1.1,
			MinRate:            10,
			MaxRate:            500,
		},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold:    5,
			SuccessThreshold:    3,
			OpenTimeout:         30 * time.Second,
			HalfOpenMaxRequests: 3,
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		TUI: TUIConfig{
			RefreshInterval: time.Second,
			Enabled:         true,
		},
	}
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	cfg := NewDefaultConfig()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)

	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config YAML: %w", err)
	}

	if err := Validate(cfg); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}
