package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cfgpkg "github.com/user/aegis/internal/config"
	poolpkg "github.com/user/aegis/internal/pool"
)

type backendSpec struct {
	name   string
	weight int
}

func TestBackendPoolNextHealthyRoundRobin(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 1},
		{name: "b", weight: 1},
	})

	want := []string{"a.example", "b.example", "a.example", "b.example"}
	for i, expected := range want {
		backend, err := pool.NextHealthy()
		if err != nil {
			t.Fatalf("NextHealthy() error = %v", err)
		}

		if got := backend.URL.Host; got != expected {
			t.Fatalf("call %d backend = %q, want %q", i, got, expected)
		}
	}
}

func TestBackendPoolSkipsUnhealthyBackends(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 1},
		{name: "b", weight: 1},
	})

	pool.MarkUnhealthy("http://a.example")

	for i := 0; i < 3; i++ {
		backend, err := pool.NextHealthy()
		if err != nil {
			t.Fatalf("NextHealthy() error = %v", err)
		}

		if got := backend.URL.Host; got != "b.example" {
			t.Fatalf("backend = %q, want %q", got, "b.example")
		}
	}
}

func TestBackendPoolReturnsErrorWhenAllBackendsAreUnhealthy(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 1},
	})

	pool.MarkUnhealthy("http://a.example")

	_, err := pool.NextHealthy()
	if err == nil {
		t.Fatal("NextHealthy() error = nil, want no healthy backends error")
	}
}

func TestBackendPoolMarkHealthyAndUnhealthy(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 2},
	})

	pool.MarkUnhealthy("http://a.example")
	backend := pool.GetAll()[0]
	if backend.Healthy.Load() {
		t.Fatal("Healthy = true, want false")
	}

	pool.MarkHealthy("http://a.example")
	if !backend.Healthy.Load() {
		t.Fatal("Healthy = false, want true")
	}

	if got := backend.WarmupLevel.Load(); got != 0 {
		t.Fatalf("WarmupLevel = %d, want 0", got)
	}
}

func TestBackendPoolWarmupLevelAffectsSelection(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 4},
		{name: "b", weight: 4},
	})

	backendA := pool.GetAll()[0]
	backendA.WarmupLevel.Store(0)

	counts := map[string]int{}
	for i := 0; i < 10; i++ {
		backend, err := pool.NextHealthy()
		if err != nil {
			t.Fatalf("NextHealthy() error = %v", err)
		}

		counts[backend.URL.Host]++
	}

	if counts["a.example"] >= counts["b.example"] {
		t.Fatalf("warm-up selection = %#v, want backend a to receive fewer requests", counts)
	}
}

func TestBackendPoolNextHealthyConcurrentAccess(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 1},
		{name: "b", weight: 1},
		{name: "c", weight: 1},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			backend, err := pool.NextHealthy()
			if err != nil {
				t.Errorf("NextHealthy() error = %v", err)
				return
			}

			if backend == nil {
				t.Error("backend = nil, want non-nil backend")
			}
		}()
	}

	wg.Wait()
}

func TestHealthChecksMarkBackendUnhealthyAndHealthyAgain(t *testing.T) {
	t.Parallel()

	var responses atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := responses.Add(1)
		if current <= 2 {
			http.Error(w, "fail", http.StatusServiceUnavailable)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pool := newHTTPPool(t, []string{server.URL})
	backend := pool.GetAll()[0]

	cfg := cfgpkg.HealthCheckConfig{
		Interval:           20 * time.Millisecond,
		Timeout:            100 * time.Millisecond,
		Path:               "/health",
		UnhealthyThreshold: 2,
		HealthyThreshold:   2,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poolpkg.StartHealthChecks(ctx, pool, cfg)

	requireEventually(t, time.Second, func() bool {
		return !backend.Healthy.Load()
	})

	requireEventually(t, time.Second, func() bool {
		return backend.Healthy.Load() && backend.WarmupLevel.Load() == 0
	})
}

func TestHealthChecksIncreaseWarmupLevelAfterThreeSuccesses(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pool := newHTTPPool(t, []string{server.URL})
	backend := pool.GetAll()[0]
	backend.WarmupLevel.Store(0)

	cfg := cfgpkg.HealthCheckConfig{
		Interval:           20 * time.Millisecond,
		Timeout:            100 * time.Millisecond,
		Path:               "/health",
		UnhealthyThreshold: 2,
		HealthyThreshold:   2,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poolpkg.StartHealthChecks(ctx, pool, cfg)

	requireEventually(t, time.Second, func() bool {
		return backend.WarmupLevel.Load() >= 1
	})
}

func TestHealthChecksRequireThreePostRecoverySuccessesBeforeWarmupAdvance(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	pool := newHTTPPool(t, []string{server.URL})
	backend := pool.GetAll()[0]
	pool.MarkUnhealthy(server.URL)

	cfg := cfgpkg.HealthCheckConfig{
		Interval:           100 * time.Millisecond,
		Timeout:            100 * time.Millisecond,
		Path:               "/health",
		UnhealthyThreshold: 1,
		HealthyThreshold:   1,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poolpkg.StartHealthChecks(ctx, pool, cfg)

	requireEventually(t, time.Second, func() bool {
		return backend.Healthy.Load() && backend.WarmupLevel.Load() == 0
	})

	requireConsistently(t, 250*time.Millisecond, func() bool {
		return backend.WarmupLevel.Load() == 0
	})

	requireEventually(t, time.Second, func() bool {
		return backend.WarmupLevel.Load() == 1
	})
}

func TestHealthChecksUsePinnedAddressForProbes(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	pool, err := poolpkg.NewPool([]cfgpkg.BackendConfig{
		{
			URL:           "http://backend.example.com",
			Weight:        1,
			PinnedAddress: parsed.Host,
			ServerName:    "backend.example.com",
			OriginalHost:  "backend.example.com",
		},
	}, testDirector)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}

	pool.MarkUnhealthy("http://backend.example.com")
	backend := pool.GetAll()[0]

	cfg := cfgpkg.HealthCheckConfig{
		Interval:           20 * time.Millisecond,
		Timeout:            100 * time.Millisecond,
		Path:               "/health",
		UnhealthyThreshold: 1,
		HealthyThreshold:   1,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poolpkg.StartHealthChecks(ctx, pool, cfg)

	requireEventually(t, time.Second, func() bool {
		return backend.Healthy.Load()
	})
}

func newTestPool(t *testing.T, specs []backendSpec) *poolpkg.BackendPool {
	t.Helper()

	configs := make([]cfgpkg.BackendConfig, 0, len(specs))
	for _, spec := range specs {
		configs = append(configs, cfgpkg.BackendConfig{
			URL:           "http://" + spec.name + ".example",
			Weight:        spec.weight,
			PinnedAddress: spec.name + ".example:80",
			ServerName:    spec.name + ".example",
			OriginalHost:  spec.name + ".example",
		})
	}

	pool, err := poolpkg.NewPool(configs, testDirector)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}

	return pool
}

func newHTTPPool(t *testing.T, rawURLs []string) *poolpkg.BackendPool {
	t.Helper()

	configs := make([]cfgpkg.BackendConfig, 0, len(rawURLs))
	for _, rawURL := range rawURLs {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("url.Parse() error = %v", err)
		}

		configs = append(configs, cfgpkg.BackendConfig{
			URL:           rawURL,
			Weight:        1,
			PinnedAddress: parsed.Host,
			ServerName:    parsed.Hostname(),
			OriginalHost:  parsed.Host,
		})
	}

	pool, err := poolpkg.NewPool(configs, testDirector)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}

	return pool
}

func testDirector(targetURL *url.URL, hostHeader string) func(*http.Request) {
	return func(req *http.Request) {
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.Host = hostHeader
	}
}

func requireEventually(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("condition was not met before timeout")
}

func requireConsistently(t *testing.T, duration time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if !fn() {
			t.Fatal("condition did not remain true for the entire interval")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

var _ *httputil.ReverseProxy
