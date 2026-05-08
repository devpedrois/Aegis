package test

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	cfgpkg "github.com/user/aegis/internal/config"
	metricspkg "github.com/user/aegis/internal/metrics"
	poolpkg "github.com/user/aegis/internal/pool"
	proxypkg "github.com/user/aegis/internal/proxy"
)

func TestPercentileReturnsMedianForP50(t *testing.T) {
	t.Parallel()

	got := metricspkg.Percentile([]time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
		4 * time.Millisecond,
		5 * time.Millisecond,
	}, 50)

	if got != 3*time.Millisecond {
		t.Fatalf("Percentile(..., 50) = %v, want %v", got, 3*time.Millisecond)
	}
}

func TestPercentileReturnsUpperBoundForP95(t *testing.T) {
	t.Parallel()

	got := metricspkg.Percentile([]time.Duration{
		1 * time.Millisecond,
		2 * time.Millisecond,
		3 * time.Millisecond,
		4 * time.Millisecond,
		5 * time.Millisecond,
	}, 95)

	if got != 5*time.Millisecond {
		t.Fatalf("Percentile(..., 95) = %v, want %v", got, 5*time.Millisecond)
	}
}

func TestPercentileReturnsZeroForEmptySlice(t *testing.T) {
	t.Parallel()

	if got := metricspkg.Percentile(nil, 50); got != 0 {
		t.Fatalf("Percentile(nil, 50) = %v, want 0", got)
	}
}

func TestPercentileReturnsOnlyElementForSingleValue(t *testing.T) {
	t.Parallel()

	if got := metricspkg.Percentile([]time.Duration{42 * time.Millisecond}, 99); got != 42*time.Millisecond {
		t.Fatalf("Percentile(singleton, 99) = %v, want %v", got, 42*time.Millisecond)
	}
}

func TestSlidingWindowDiscardsEntriesOlderThanMaxAge(t *testing.T) {
	t.Parallel()

	window := metricspkg.NewSlidingWindow(60*time.Second, 10)
	now := time.Now()

	window.Record(metricspkg.LatencyEntry{
		Latency:   900 * time.Millisecond,
		Timestamp: now.Add(-61 * time.Second),
	})
	window.Record(metricspkg.LatencyEntry{
		Latency:   100 * time.Millisecond,
		Timestamp: now.Add(-1 * time.Second),
	})

	snapshot := window.Snapshot(now)

	if snapshot.Count != 1 {
		t.Fatalf("Snapshot().Count = %d, want %d", snapshot.Count, 1)
	}

	if snapshot.P95 != 100*time.Millisecond {
		t.Fatalf("Snapshot().P95 = %v, want %v", snapshot.P95, 100*time.Millisecond)
	}
}

func TestSlidingWindowRespectsMaxEntries(t *testing.T) {
	t.Parallel()

	window := metricspkg.NewSlidingWindow(time.Minute, 2)
	now := time.Now()

	window.Record(metricspkg.LatencyEntry{Latency: 10 * time.Millisecond, Timestamp: now.Add(-3 * time.Second)})
	window.Record(metricspkg.LatencyEntry{Latency: 20 * time.Millisecond, Timestamp: now.Add(-2 * time.Second)})
	window.Record(metricspkg.LatencyEntry{Latency: 30 * time.Millisecond, Timestamp: now.Add(-1 * time.Second)})

	snapshot := window.Snapshot(now)

	if snapshot.Count != 2 {
		t.Fatalf("Snapshot().Count = %d, want %d", snapshot.Count, 2)
	}

	if snapshot.P50 != 20*time.Millisecond {
		t.Fatalf("Snapshot().P50 = %v, want %v", snapshot.P50, 20*time.Millisecond)
	}

	if snapshot.P95 != 30*time.Millisecond {
		t.Fatalf("Snapshot().P95 = %v, want %v", snapshot.P95, 30*time.Millisecond)
	}
}

func TestMetricsCollectorRecordsLatenciesConcurrently(t *testing.T) {
	t.Parallel()

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)

	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func(index int) {
			collector.RecordLatency("http://backend.example", time.Duration(index+1)*time.Millisecond, index%10 == 0)
			done <- struct{}{}
		}(i)
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	snapshot := collector.Snapshot()
	if len(snapshot.BackendStats) != 1 {
		t.Fatalf("len(BackendStats) = %d, want %d", len(snapshot.BackendStats), 1)
	}

	backend := snapshot.BackendStats[0]
	if backend.TotalRequests != 100 {
		t.Fatalf("TotalRequests = %d, want %d", backend.TotalRequests, 100)
	}

	if backend.TotalErrors != 10 {
		t.Fatalf("TotalErrors = %d, want %d", backend.TotalErrors, 10)
	}
}

func TestMetricsCollectorSnapshotIsStableWithBackendRuntimeData(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	backend := pool.GetAll()[0]
	backend.TotalRequests.Store(2)
	backend.TotalErrors.Store(1)
	backend.ActiveRequests.Store(3)

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())
	collector.RecordLatency(backend.URL.String(), 100*time.Millisecond, false)
	collector.RecordLatency(backend.URL.String(), 200*time.Millisecond, true)

	snapshot := collector.Snapshot()

	if snapshot.TotalReqPerSec <= 0 {
		t.Fatalf("TotalReqPerSec = %v, want > 0", snapshot.TotalReqPerSec)
	}

	if len(snapshot.BackendStats) != 1 {
		t.Fatalf("len(BackendStats) = %d, want %d", len(snapshot.BackendStats), 1)
	}

	got := snapshot.BackendStats[0]
	if got.URL != backend.URL.String() {
		t.Fatalf("URL = %q, want %q", got.URL, backend.URL.String())
	}

	if !got.Healthy {
		t.Fatal("Healthy = false, want true")
	}

	if got.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want %d", got.TotalRequests, 2)
	}

	if got.TotalErrors != 1 {
		t.Fatalf("TotalErrors = %d, want %d", got.TotalErrors, 1)
	}

	if got.ActiveRequests != 3 {
		t.Fatalf("ActiveRequests = %d, want %d", got.ActiveRequests, 3)
	}

	if math.Abs(got.ErrorRate-0.5) > 0.001 {
		t.Fatalf("ErrorRate = %v, want %v", got.ErrorRate, 0.5)
	}

	if got.LatencyP50 != 100*time.Millisecond {
		t.Fatalf("LatencyP50 = %v, want %v", got.LatencyP50, 100*time.Millisecond)
	}

	if got.LatencyP95 != 200*time.Millisecond {
		t.Fatalf("LatencyP95 = %v, want %v", got.LatencyP95, 200*time.Millisecond)
	}
}

func TestMetricsCollectorSnapshotHandlesEmptyState(t *testing.T) {
	t.Parallel()

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	snapshot := collector.Snapshot()

	if snapshot.TotalReqPerSec != 0 {
		t.Fatalf("TotalReqPerSec = %v, want 0", snapshot.TotalReqPerSec)
	}

	if len(snapshot.BackendStats) != 0 {
		t.Fatalf("len(BackendStats) = %d, want 0", len(snapshot.BackendStats))
	}
}

func TestMetricsCollectorSnapshotIncludesCircuitState(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	backend := pool.GetAll()[0]
	backend.CircuitBreaker = newTestBreaker(time.Second, 1, 2, 2)
	backend.CircuitBreaker.RecordFailure()

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())

	snapshot := collector.Snapshot()
	if len(snapshot.BackendStats) != 1 {
		t.Fatalf("len(BackendStats) = %d, want %d", len(snapshot.BackendStats), 1)
	}

	if got := snapshot.BackendStats[0].CircuitState; got != "open" {
		t.Fatalf("CircuitState = %q, want %q", got, "open")
	}
}

func TestMetricsCollectorSnapshotConcurrentWithControlStateUpdates(t *testing.T) {
	t.Parallel()

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)

		go func(index int) {
			defer wg.Done()
			collector.SetControlState(float64(index), index, "normal")
		}(i)

		go func() {
			defer wg.Done()
			_ = collector.Snapshot()
		}()
	}

	wg.Wait()
}

func TestStartPeriodicLoggingEmitsAggregateMetrics(t *testing.T) {
	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	backend := pool.GetAll()[0]
	backend.TotalRequests.Store(1)

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())
	collector.RecordLatency(backend.URL.String(), 150*time.Millisecond, false)

	buffer := &lockedBuffer{}
	restoreLogger := metricspkg.SetLoggerOutput(buffer)
	defer restoreLogger()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	metricspkg.StartPeriodicLoggingWithLevel(ctx, collector, 20*time.Millisecond, "info")
	requireEventually(t, time.Second, func() bool {
		output := buffer.String()
		return strings.Contains(output, "INFO Metrics:") && strings.Contains(output, backend.URL.Host)
	})
}

func TestInstrumentedTransportMeasuresLatencyAndUpdatesCollector(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	pool := newHTTPPool(t, []string{server.URL})
	backend := pool.GetAll()[0]
	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())

	transport := proxypkg.NewInstrumentedTransport(http.DefaultTransport, collector, backend)
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}

	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", string(body), "ok")
	}

	snapshot := collector.Snapshot()
	if len(snapshot.BackendStats) != 1 {
		t.Fatalf("len(BackendStats) = %d, want %d", len(snapshot.BackendStats), 1)
	}

	got := snapshot.BackendStats[0]
	if got.TotalRequests != 1 {
		t.Fatalf("TotalRequests = %d, want %d", got.TotalRequests, 1)
	}

	if got.TotalErrors != 0 {
		t.Fatalf("TotalErrors = %d, want 0", got.TotalErrors)
	}

	if got.LatencyP95 < 90*time.Millisecond {
		t.Fatalf("LatencyP95 = %v, want >= %v", got.LatencyP95, 90*time.Millisecond)
	}
}

func TestInstrumentedTransportCountsServerErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	pool := newHTTPPool(t, []string{server.URL})
	backend := pool.GetAll()[0]
	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())

	transport := proxypkg.NewInstrumentedTransport(http.DefaultTransport, collector, backend)
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	resp.Body.Close()

	snapshot := collector.Snapshot()
	got := snapshot.BackendStats[0]
	if got.TotalRequests != 1 {
		t.Fatalf("TotalRequests = %d, want %d", got.TotalRequests, 1)
	}

	if got.TotalErrors != 1 {
		t.Fatalf("TotalErrors = %d, want %d", got.TotalErrors, 1)
	}
}

func TestInstrumentedTransportCountsUpstreamRoundTripErrors(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	backend := pool.GetAll()[0]
	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())

	transport := proxypkg.NewInstrumentedTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial failure")
	}), collector, backend)

	req, err := http.NewRequest(http.MethodGet, "http://a.example", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip() error = nil, want upstream error")
	}

	snapshot := collector.Snapshot()
	got := snapshot.BackendStats[0]
	if got.TotalRequests != 0 {
		t.Fatalf("TotalRequests = %d, want 0", got.TotalRequests)
	}

	if got.TotalErrors != 1 {
		t.Fatalf("TotalErrors = %d, want %d", got.TotalErrors, 1)
	}
}

func TestHealthChecksDoNotPolluteMetricsWithoutClientTraffic(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			time.Sleep(80 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			return
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	pool := newHTTPPoolWithMetrics(t, []string{server.URL}, collector)
	collector.SetBackends(pool.GetAll())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poolpkg.StartHealthChecks(ctx, pool, cfgpkg.HealthCheckConfig{
		Interval:           20 * time.Millisecond,
		Timeout:            200 * time.Millisecond,
		Path:               "/health",
		UnhealthyThreshold: 2,
		HealthyThreshold:   1,
	})

	time.Sleep(120 * time.Millisecond)

	snapshot := collector.Snapshot()
	if len(snapshot.BackendStats) != 1 {
		t.Fatalf("len(BackendStats) = %d, want 1", len(snapshot.BackendStats))
	}

	got := snapshot.BackendStats[0]
	if got.TotalRequests != 0 {
		t.Fatalf("TotalRequests = %d, want 0 without client traffic", got.TotalRequests)
	}

	if got.TotalErrors != 0 {
		t.Fatalf("TotalErrors = %d, want 0 without client traffic", got.TotalErrors)
	}

	if got.ReqPerSec != 0 {
		t.Fatalf("ReqPerSec = %v, want 0 without client traffic", got.ReqPerSec)
	}

	if got.LatencyP95 != 0 {
		t.Fatalf("LatencyP95 = %v, want 0 without client traffic", got.LatencyP95)
	}
}

func TestHealthChecksDoNotPolluteErrorMetricsOnProbeFailures(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unhealthy", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	pool := newHTTPPoolWithMetrics(t, []string{server.URL}, collector)
	collector.SetBackends(pool.GetAll())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	poolpkg.StartHealthChecks(ctx, pool, cfgpkg.HealthCheckConfig{
		Interval:           20 * time.Millisecond,
		Timeout:            200 * time.Millisecond,
		Path:               "/health",
		UnhealthyThreshold: 1,
		HealthyThreshold:   1,
	})

	requireEventually(t, time.Second, func() bool {
		return !pool.GetAll()[0].Healthy.Load()
	})

	snapshot := collector.Snapshot()
	if len(snapshot.BackendStats) != 1 {
		t.Fatalf("len(BackendStats) = %d, want 1", len(snapshot.BackendStats))
	}

	got := snapshot.BackendStats[0]
	if got.TotalErrors != 0 {
		t.Fatalf("TotalErrors = %d, want 0 when only health probes failed", got.TotalErrors)
	}

	if got.ReqPerSec != 0 {
		t.Fatalf("ReqPerSec = %v, want 0 when only health probes ran", got.ReqPerSec)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func newHTTPPoolWithMetrics(t *testing.T, rawURLs []string, collector *metricspkg.MetricsCollector) *poolpkg.BackendPool {
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

	pool, err := poolpkg.NewPool(configs, testDirector, proxypkg.NewInstrumentedTransportFactory(collector))
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}

	return pool
}

var _ *poolpkg.Backend
