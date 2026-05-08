package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	cfgpkg "github.com/user/aegis/internal/config"
	logpkg "github.com/user/aegis/internal/logging"
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

func TestRunWithContextShutsDownCleanlyOnCancel(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	parsedBackendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	logger, err := logpkg.NewLogger(logpkg.Config{Level: "error", Format: "text"}, io.Discard)
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	slog.SetDefault(logger)

	cfg := cfgpkg.NewDefaultConfig()
	cfg.TUI.Enabled = false
	cfg.Server.Port = 0
	cfg.Server.ShutdownTimeout = time.Second
	cfg.Backends = []cfgpkg.BackendConfig{
		{
			URL:           backend.URL,
			Weight:        1,
			PinnedAddress: parsedBackendURL.Host,
			ServerName:    parsedBackendURL.Hostname(),
			OriginalHost:  parsedBackendURL.Host,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	eventBuffer := logpkg.NewEventBuffer(10)
	done := make(chan error, 1)
	go func() {
		done <- runWithContext(ctx, cancel, cfg, eventBuffer)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWithContext() error = %v, want nil on graceful cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runWithContext() did not return after cancel")
	}
}
