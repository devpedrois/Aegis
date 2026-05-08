package main

import (
	"net/url"
	"testing"
	"time"

	cfgpkg "github.com/user/aegis/internal/config"
	poolpkg "github.com/user/aegis/internal/pool"
)

func TestAttachCircuitBreakersAssignsConfiguredBreakers(t *testing.T) {
	t.Parallel()

	backendURL, err := url.Parse("http://backend.example")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	backends := []*poolpkg.Backend{
		{URL: backendURL},
	}

	attachCircuitBreakers(backends, cfgpkg.CircuitBreakerConfig{
		FailureThreshold:    1,
		SuccessThreshold:    2,
		OpenTimeout:         time.Second,
		HalfOpenMaxRequests: 1,
	})

	if backends[0].CircuitBreaker == nil {
		t.Fatal("CircuitBreaker = nil, want initialized breaker")
	}

	backends[0].CircuitBreaker.RecordFailure()
	if backends[0].CircuitBreaker.AllowRequest() {
		t.Fatal("AllowRequest() = true, want false after configured failure threshold")
	}
}
