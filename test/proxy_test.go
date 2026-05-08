package test

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	circuitpkg "github.com/user/aegis/internal/circuit"
	cfgpkg "github.com/user/aegis/internal/config"
	metricspkg "github.com/user/aegis/internal/metrics"
	poolpkg "github.com/user/aegis/internal/pool"
	proxypkg "github.com/user/aegis/internal/proxy"
	ratelimitpkg "github.com/user/aegis/internal/ratelimit"
	securitypkg "github.com/user/aegis/internal/security"
)

func TestProxyHandlerUsesHealthyPoolBackends(t *testing.T) {
	t.Parallel()

	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"backend": "a"})
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"backend": "b"})
	}))
	defer backendB.Close()

	pool := newHTTPPool(t, []string{backendA.URL, backendB.URL})
	handler := proxypkg.NewProxyHandler(pool)

	server := httptest.NewServer(handler)
	defer server.Close()

	want := []string{"a", "b", "a", "b"}
	for i, expectedBackend := range want {
		resp, err := http.Get(server.URL + "/test")
		if err != nil {
			t.Fatalf("request %d error = %v", i, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}

		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}

		if payload["backend"] != expectedBackend {
			t.Fatalf("request %d backend = %q, want %q", i, payload["backend"], expectedBackend)
		}
	}
}

func TestProxyHandlerReturns503WhenNoHealthyBackendExists(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	pool.MarkUnhealthy("http://a.example")

	handler := proxypkg.NewProxyHandler(pool)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:4567"

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("ServeHTTP() status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}

	if got := strings.TrimSpace(recorder.Body.String()); got != `{"error":"no healthy backends"}` {
		t.Fatalf("ServeHTTP() body = %q, want generic 503 body", got)
	}
}

func TestProxyRoundRobinDistributionEndToEnd(t *testing.T) {
	backends := []testBackendConfig{
		{Name: "backend-a", Weight: 1, Control: newTestBackendControl()},
		{Name: "backend-b", Weight: 1, Control: newTestBackendControl()},
		{Name: "backend-c", Weight: 1, Control: newTestBackendControl()},
	}

	server, _, _, cleanup := newTestProxy(t, backends)
	defer cleanup()

	counts := make(map[string]int)
	for i := 0; i < 30; i++ {
		response := sendProxyRequest(t, server, "/round-robin")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want %d", i, response.StatusCode, http.StatusOK)
		}

		counts[response.Backend]++
		time.Sleep(2 * time.Millisecond)
	}

	for backendName, got := range counts {
		if got < 8 || got > 12 {
			t.Fatalf("backend %s handled %d requests, want approximately 10", backendName, got)
		}
	}
}

func TestProxyBackendFailureRedistributesTrafficEndToEnd(t *testing.T) {
	backendA := newTestBackendControl()
	backendB := newTestBackendControl()
	backendC := newTestBackendControl()
	backends := []testBackendConfig{
		{Name: "backend-a", Weight: 1, Control: backendA},
		{Name: "backend-b", Weight: 1, Control: backendB},
		{Name: "backend-c", Weight: 1, Control: backendC},
	}

	server, _, _, cleanup := newTestProxy(t, backends)
	defer cleanup()

	backendA.SetAvailable(false)
	requireEventually(t, time.Second, func() bool {
		return backendA.Backend() != nil && !backendA.Backend().Healthy.Load()
	})

	backendA.ResetRequests()
	backendB.ResetRequests()
	backendC.ResetRequests()

	for i := 0; i < 18; i++ {
		response := sendProxyRequest(t, server, "/redistribute")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want %d", i, response.StatusCode, http.StatusOK)
		}

		time.Sleep(2 * time.Millisecond)
	}

	if got := backendA.RequestCount(); got != 0 {
		t.Fatalf("failed backend handled %d requests after removal, want 0", got)
	}

	for _, control := range []*testBackendControl{backendB, backendC} {
		if got := control.RequestCount(); got < 7 || got > 11 {
			t.Fatalf("healthy backend handled %d requests, want approximately 9", got)
		}
	}
}

type testBackendConfig struct {
	Name    string
	Weight  int
	Latency time.Duration
	FailRate float64
	Control *testBackendControl
}

type testProxyResponse struct {
	StatusCode int
	Backend    string
	Body       string
}

type testBackendControl struct {
	available atomic.Bool
	latencyNS atomic.Int64
	requests  atomic.Int64
	sequence  atomic.Int64
	mu        sync.RWMutex
	failRate  float64
	backend   *poolpkg.Backend
}

func newTestBackendControl() *testBackendControl {
	control := &testBackendControl{}
	control.available.Store(true)
	return control
}

func (c *testBackendControl) SetAvailable(available bool) {
	c.available.Store(available)
}

func (c *testBackendControl) SetLatency(latency time.Duration) {
	c.latencyNS.Store(int64(latency))
}

func (c *testBackendControl) SetFailRate(rate float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.failRate = rate
}

func (c *testBackendControl) ResetRequests() {
	c.requests.Store(0)
	c.sequence.Store(0)
}

func (c *testBackendControl) RequestCount() int64 {
	return c.requests.Load()
}

func (c *testBackendControl) Backend() *poolpkg.Backend {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.backend
}

func (c *testBackendControl) bindBackend(backend *poolpkg.Backend) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.backend = backend
}

func (c *testBackendControl) currentFailRate() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.failRate
}

func (c *testBackendControl) shouldFail() bool {
	failRate := c.currentFailRate()
	if failRate <= 0 {
		return false
	}

	if failRate >= 1 {
		return true
	}

	failEvery := int64(math.Round(1 / failRate))
	if failEvery <= 1 {
		return true
	}

	sequence := c.sequence.Add(1)
	return sequence%failEvery == 0
}

func newTestProxy(t *testing.T, backends []testBackendConfig) (*http.Server, *metricspkg.MetricsCollector, *ratelimitpkg.RateLimiter, func()) {
	t.Helper()

	if len(backends) == 0 {
		t.Fatal("newTestProxy() requires at least one backend")
	}

	backendServers := make([]*httptest.Server, 0, len(backends))
	poolConfigs := make([]cfgpkg.BackendConfig, 0, len(backends))
	controls := make([]*testBackendControl, 0, len(backends))

	for _, backend := range backends {
		control := backend.Control
		if control == nil {
			control = newTestBackendControl()
		}

		if backend.Latency > 0 {
			control.SetLatency(backend.Latency)
		}

		if backend.FailRate > 0 {
			control.SetFailRate(backend.FailRate)
		}

		name := backend.Name
		if name == "" {
			name = "backend"
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				if !control.available.Load() {
					http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
					return
				}

				w.WriteHeader(http.StatusOK)
				return
			}

			if !control.available.Load() {
				http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
				return
			}

			control.requests.Add(1)

			latency := time.Duration(control.latencyNS.Load())
			if latency > 0 {
				time.Sleep(latency)
			}

			if control.shouldFail() {
				http.Error(w, "backend failure", http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"backend": name})
		}))
		backendServers = append(backendServers, server)
		controls = append(controls, control)

		parsedURL, err := url.Parse(server.URL)
		if err != nil {
			t.Fatalf("url.Parse() error = %v", err)
		}

		weight := backend.Weight
		if weight <= 0 {
			weight = 1
		}

		poolConfigs = append(poolConfigs, cfgpkg.BackendConfig{
			URL:           server.URL,
			Weight:        weight,
			PinnedAddress: parsedURL.Host,
			ServerName:    parsedURL.Hostname(),
			OriginalHost:  parsedURL.Host,
		})
	}

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	backendPool, err := poolpkg.NewPool(poolConfigs, proxypkg.NewDirector, proxypkg.NewInstrumentedTransportFactory(collector))
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}

	attachTestCircuitBreakers(backendPool.GetAll())
	collector.SetBackends(backendPool.GetAll())

	for index, control := range controls {
		control.bindBackend(backendPool.GetAll()[index])
	}

	ctx, cancel := context.WithCancel(context.Background())
	poolpkg.StartHealthChecks(ctx, backendPool, cfgpkg.HealthCheckConfig{
		Interval:           20 * time.Millisecond,
		Timeout:            100 * time.Millisecond,
		Path:               "/health",
		UnhealthyThreshold: 2,
		HealthyThreshold:   1,
	})

	proxyHandler := proxypkg.NewProxyHandler(backendPool)
	limiter := ratelimitpkg.NewRateLimiter(1000, 20)
	ratelimitpkg.StartCleanup(ctx, limiter, time.Hour)

	// [SECURITY] The integration harness preserves the production edge order so hostile requests still cross validation before backend routing.
	handler := securitypkg.SecurityHeaders(
		proxypkg.RequestLogger(
			proxypkg.Recovery(
				securitypkg.ValidateRequest(securitypkg.RequestValidationConfig{
					MaxBodyBytes: 1 << 20,
				})(
					limiter.Middleware(proxyHandler),
				),
			),
		),
	)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}

	server := &http.Server{
		Addr:           listener.Addr().String(),
		Handler:        handler,
		ReadTimeout:    2 * time.Second,
		WriteTimeout:   2 * time.Second,
		IdleTimeout:    30 * time.Second,
		MaxHeaderBytes: 8192,
	}

	serveDone := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			serveDone <- err
			return
		}

		serveDone <- nil
	}()

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()

			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
			defer shutdownCancel()

			_ = server.Shutdown(shutdownCtx)
			_ = listener.Close()

			select {
			case err := <-serveDone:
				if err != nil {
					t.Fatalf("server.Serve() error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("test proxy server did not stop before timeout")
			}

			for _, backendServer := range backendServers {
				backendServer.Close()
			}
		})
	}

	return server, collector, limiter, cleanup
}

func attachTestCircuitBreakers(backends []*poolpkg.Backend) {
	for _, backend := range backends {
		if backend == nil {
			continue
		}

		backend.CircuitBreaker = circuitpkg.NewCircuitBreaker(cfgpkg.CircuitBreakerConfig{
			FailureThreshold:    3,
			SuccessThreshold:    2,
			OpenTimeout:         time.Second,
			HalfOpenMaxRequests: 1,
		})
		if backend.URL != nil {
			backend.CircuitBreaker.SetBackendName(backend.URL.String())
		}
	}
}

func sendProxyRequest(t *testing.T, server *http.Server, requestPath string) testProxyResponse {
	t.Helper()

	requestURL := "http://" + server.Addr + requestPath
	response, err := http.Get(requestURL)
	if err != nil {
		t.Fatalf("http.Get() error = %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}

	payload := map[string]string{}
	if response.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
	}

	return testProxyResponse{
		StatusCode: response.StatusCode,
		Backend:    payload["backend"],
		Body:       string(body),
	}
}

func TestProxyHealthCheckRecoveryWarmupEndToEnd(t *testing.T) {
	backendA := newTestBackendControl()
	backendB := newTestBackendControl()
	backends := []testBackendConfig{
		{Name: "backend-a", Weight: 1, Control: backendA},
		{Name: "backend-b", Weight: 1, Control: backendB},
	}

	server, _, _, cleanup := newTestProxy(t, backends)
	defer cleanup()

	backendA.SetAvailable(false)
	requireEventually(t, time.Second, func() bool {
		return backendA.Backend() != nil && !backendA.Backend().Healthy.Load()
	})

	backendA.SetAvailable(true)
	requireEventually(t, time.Second, func() bool {
		return backendA.Backend() != nil && backendA.Backend().Healthy.Load() && backendA.Backend().WarmupLevel.Load() == 0
	})

	phaseCounts := make([]int64, 0, 4)
	for warmupLevel := int32(0); warmupLevel <= 3; warmupLevel++ {
		requireEventually(t, time.Second, func() bool {
			return backendA.Backend() != nil && backendA.Backend().WarmupLevel.Load() == warmupLevel
		})

		backendA.ResetRequests()
		backendB.ResetRequests()

		for i := 0; i < 10; i++ {
			response := sendProxyRequest(t, server, "/recovery")
			if response.StatusCode != http.StatusOK {
				t.Fatalf("warm-up level %d request %d status = %d, want %d", warmupLevel, i, response.StatusCode, http.StatusOK)
			}
		}

		phaseCounts = append(phaseCounts, backendA.RequestCount())
	}

	for i := 1; i < len(phaseCounts); i++ {
		if phaseCounts[i] <= phaseCounts[i-1] {
			t.Fatalf("warm-up counts = %v, want strictly increasing recovery traffic", phaseCounts)
		}
	}

	if phaseCounts[len(phaseCounts)-1] < 4 {
		t.Fatalf("final warm-up count = %d, want backend fully restored near half of 10 requests", phaseCounts[len(phaseCounts)-1])
	}
}

func TestProxyRateLimitEnforcementEndToEnd(t *testing.T) {
	backends := []testBackendConfig{
		{Name: "backend-a", Weight: 1, Control: newTestBackendControl()},
	}

	server, _, limiter, cleanup := newTestProxy(t, backends)
	defer cleanup()

	limiter.SetGlobalRate(10)

	tooManyRequests := 0
	for i := 0; i < 100; i++ {
		response := sendProxyRequest(t, server, "/rate-limit")
		if response.StatusCode == http.StatusTooManyRequests {
			tooManyRequests++
		}
	}

	if tooManyRequests < 70 {
		t.Fatalf("429 count = %d, want majority of requests rejected", tooManyRequests)
	}
}

func TestProxyCircuitBreakerOpenStopsRoutingToFailingBackend(t *testing.T) {
	backendA := newTestBackendControl()
	backends := []testBackendConfig{
		{Name: "backend-a", Weight: 1, FailRate: 1, Control: backendA},
	}

	server, _, _, cleanup := newTestProxy(t, backends)
	defer cleanup()

	for i := 0; i < 12; i++ {
		_ = sendProxyRequest(t, server, "/circuit")
		time.Sleep(2 * time.Millisecond)
	}

	requireEventually(t, time.Second, func() bool {
		return backendA.Backend() != nil && backendA.Backend().CircuitBreaker != nil && backendA.Backend().CircuitBreaker.State().String() == "open"
	})

	failingCountBefore := backendA.RequestCount()
	for i := 0; i < 5; i++ {
		response := sendProxyRequest(t, server, "/circuit")
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("post-open request %d status = %d, want %d", i, response.StatusCode, http.StatusServiceUnavailable)
		}
	}

	if got := backendA.RequestCount(); got != failingCountBefore {
		t.Fatalf("failing backend requests = %d, want frozen at %d after circuit open", got, failingCountBefore)
	}
}
