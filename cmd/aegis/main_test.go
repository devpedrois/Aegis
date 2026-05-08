package main

import (
	"context"
	"errors"
	"io"
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

func TestLogWriterDiscardsOutputWhenTUIIsEnabled(t *testing.T) {
	t.Parallel()

	if got := logWriter(true); got != io.Discard {
		t.Fatalf("logWriter(true) = %T, want io.Discard", got)
	}
}

func TestWaitForExitContinuesWhenTUIFails(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverErr := make(chan error, 1)
	tuiErr := make(chan error, 1)
	tuiErr <- errors.New("terminal unavailable")

	done := make(chan error, 1)
	go func() {
		done <- waitForExit(ctx, cancel, serverErr, tuiErr, true)
	}()

	select {
	case err := <-done:
		t.Fatalf("waitForExit() returned early with %v, want headless fallback", err)
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForExit() error = %v, want nil after cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waitForExit() did not return after cancel")
	}
}

func TestWaitForExitCancelsWhenTUIQuitsCleanly(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serverErr := make(chan error, 1)
	tuiErr := make(chan error, 1)
	tuiErr <- nil

	if err := waitForExit(ctx, cancel, serverErr, tuiErr, true); err != nil {
		t.Fatalf("waitForExit() error = %v, want nil", err)
	}

	select {
	case <-ctx.Done():
	default:
		t.Fatal("ctx not canceled after clean TUI quit")
	}
}
