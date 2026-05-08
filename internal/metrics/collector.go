package metrics

import (
	"context"
	"io"
	"log"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/user/aegis/internal/pool"
)

type MetricsCollector struct {
	windows       map[string]*SlidingWindow
	backendMap    map[string]*pool.Backend
	maxAge        time.Duration
	maxEntries    int
	currentRate   float64
	blockedIPs    int
	adaptiveState string
	// Aggregate metrics state is protected by RWMutex so concurrent requests cannot corrupt shared accounting under load.
	mu sync.RWMutex
}

type SlidingWindow struct {
	entries    []LatencyEntry
	start      int
	count      int
	maxAge     time.Duration
	maxEntries int
	// Each backend window has its own mutex so concurrent updates do not corrupt the ring buffer.
	mu sync.Mutex
}

type LatencyEntry struct {
	Latency   time.Duration
	Timestamp time.Time
	IsError   bool
}

var metricsLogger = log.New(os.Stderr, "", log.LstdFlags)

func NewMetricsCollector(maxAge time.Duration, maxEntries int) *MetricsCollector {
	if maxAge <= 0 {
		maxAge = time.Minute
	}

	if maxEntries <= 0 {
		maxEntries = 1000
	}

	return &MetricsCollector{
		windows:    make(map[string]*SlidingWindow),
		backendMap: make(map[string]*pool.Backend),
		maxAge:     maxAge,
		maxEntries: maxEntries,
	}
}

func NewSlidingWindow(maxAge time.Duration, maxEntries int) *SlidingWindow {
	if maxAge <= 0 {
		maxAge = time.Minute
	}

	if maxEntries <= 0 {
		maxEntries = 1000
	}

	return &SlidingWindow{
		maxAge:     maxAge,
		maxEntries: maxEntries,
	}
}

func (c *MetricsCollector) SetBackends(backends []*pool.Backend) {
	c.mu.Lock()
	defer c.mu.Unlock()

	next := make(map[string]*pool.Backend, len(backends))
	for _, backend := range backends {
		if backend == nil || backend.URL == nil {
			continue
		}

		next[backend.URL.String()] = backend
	}

	c.backendMap = next
}

func (c *MetricsCollector) RecordLatency(backendURL string, latency time.Duration, isError bool) {
	c.recordLatencyAt(backendURL, latency, isError, time.Now())
}

func (c *MetricsCollector) Snapshot() MetricsSnapshot {
	return c.snapshotAt(time.Now())
}

func (c *MetricsCollector) LatencySamples(backendURL string) []time.Duration {
	if c == nil || backendURL == "" {
		return nil
	}

	c.mu.RLock()
	window := c.windows[backendURL]
	c.mu.RUnlock()
	if window == nil {
		return nil
	}

	return window.LatencySamples(time.Now())
}

func (c *MetricsCollector) SetControlState(currentRate float64, blockedIPs int, adaptiveState string) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.currentRate = currentRate
	c.blockedIPs = blockedIPs
	c.adaptiveState = adaptiveState
}

func (c *MetricsCollector) recordLatencyAt(backendURL string, latency time.Duration, isError bool, now time.Time) {
	if c == nil || backendURL == "" {
		return
	}

	window := c.getOrCreateWindow(backendURL)
	// [SECURITY] Metrics are keyed by trusted backend URL only, never by client-controlled paths or headers, to avoid cardinality abuse.
	window.Record(LatencyEntry{
		Latency:   latency,
		Timestamp: now,
		IsError:   isError,
	})
}

func (c *MetricsCollector) getOrCreateWindow(backendURL string) *SlidingWindow {
	c.mu.RLock()
	window, ok := c.windows[backendURL]
	c.mu.RUnlock()
	if ok {
		return window
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if window, ok := c.windows[backendURL]; ok {
		return window
	}

	window = NewSlidingWindow(c.maxAge, c.maxEntries)
	c.windows[backendURL] = window
	return window
}

func (c *MetricsCollector) snapshotAt(now time.Time) MetricsSnapshot {
	if c == nil {
		return MetricsSnapshot{Timestamp: now}
	}

	c.mu.RLock()
	backendMap := make(map[string]*pool.Backend, len(c.backendMap))
	for key, backend := range c.backendMap {
		backendMap[key] = backend
	}

	windowMap := make(map[string]*SlidingWindow, len(c.windows))
	for key, window := range c.windows {
		windowMap[key] = window
	}
	currentRate := c.currentRate
	blockedIPs := c.blockedIPs
	adaptiveState := c.adaptiveState
	c.mu.RUnlock()

	urls := make([]string, 0, len(backendMap)+len(windowMap))
	seen := make(map[string]struct{}, len(backendMap)+len(windowMap))
	for key := range backendMap {
		urls = append(urls, key)
		seen[key] = struct{}{}
	}

	for key := range windowMap {
		if _, ok := seen[key]; ok {
			continue
		}

		urls = append(urls, key)
	}

	sort.Strings(urls)

	snapshot := MetricsSnapshot{
		Timestamp:     now,
		BackendStats:  make([]BackendSnapshot, 0, len(urls)),
		CurrentRate:   currentRate,
		BlockedIPs:    blockedIPs,
		AdaptiveState: adaptiveState,
	}

	for _, backendURL := range urls {
		windowSnapshot := WindowSnapshot{}
		if window := windowMap[backendURL]; window != nil {
			windowSnapshot = window.Snapshot(now)
		}

		backendSnapshot := BackendSnapshot{
			URL:          backendURL,
			CircuitState: "n/a",
			ReqPerSec:    windowSnapshot.ReqPerSec,
			LatencyP50:   windowSnapshot.P50,
			LatencyP95:   windowSnapshot.P95,
			LatencyP99:   windowSnapshot.P99,
			ErrorRate:    windowSnapshot.ErrorRate,
		}

		if backend := backendMap[backendURL]; backend != nil {
			backendSnapshot.Healthy = backend.Healthy.Load()
			backendSnapshot.ActiveRequests = backend.ActiveRequests.Load()
			backendSnapshot.TotalRequests = backend.TotalRequests.Load()
			backendSnapshot.TotalErrors = backend.TotalErrors.Load()
			if backend.CircuitBreaker != nil {
				backendSnapshot.CircuitState = backend.CircuitBreaker.State().String()
			}
			backend.LatencyP50.Store(windowSnapshot.P50.Microseconds())
			backend.LatencyP95.Store(windowSnapshot.P95.Microseconds())
			backend.LatencyP99.Store(windowSnapshot.P99.Microseconds())
		} else {
			backendSnapshot.TotalRequests = int64(windowSnapshot.Count)
			backendSnapshot.TotalErrors = int64(windowSnapshot.ErrorCount)
		}

		snapshot.TotalReqPerSec += backendSnapshot.ReqPerSec
		snapshot.BackendStats = append(snapshot.BackendStats, backendSnapshot)
	}

	return snapshot
}

func StartPeriodicLogging(ctx context.Context, collector *MetricsCollector, interval time.Duration) {
	StartPeriodicLoggingWithLevel(ctx, collector, interval, "info")
}

func StartPeriodicLoggingWithLevel(ctx context.Context, collector *MetricsCollector, interval time.Duration, level string) {
	if collector == nil || interval <= 0 || !shouldEmitInfoLogs(level) {
		return
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snapshot := collector.Snapshot()
				metricsLogger.Printf("INFO Metrics: %.2f req/s%s", snapshot.TotalReqPerSec, formatBackendMetrics(snapshot.BackendStats))
			}
		}
	}()
}

func SetLoggerOutput(writer io.Writer) func() {
	if writer == nil {
		return func() {}
	}

	previous := metricsLogger.Writer()
	metricsLogger.SetOutput(writer)

	return func() {
		metricsLogger.SetOutput(previous)
	}
}

func formatBackendMetrics(backends []BackendSnapshot) string {
	if len(backends) == 0 {
		return ""
	}

	parts := make([]string, 0, len(backends))
	for _, backend := range backends {
		parts = append(parts, backendLogLabel(backend.URL)+": P95="+backend.LatencyP95.Truncate(time.Millisecond).String()+" healthy="+formatBool(backend.Healthy))
	}

	return " | " + strings.Join(parts, " | ")
}

func backendLogLabel(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return rawURL
	}

	// [SECURITY] Periodic metrics logs use the backend host only so paths, queries, and embedded secrets are never written to shared log sinks.
	return parsed.Host
}

func formatBool(value bool) string {
	if value {
		return "true"
	}

	return "false"
}

func (w *SlidingWindow) Record(entry LatencyEntry) {
	if w == nil {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.entries) < w.maxEntries {
		w.entries = append(w.entries, entry)
		w.count++
		return
	}

	w.entries[w.start] = entry
	w.start = (w.start + 1) % w.maxEntries
}

func (w *SlidingWindow) LatencySamples(now time.Time) []time.Duration {
	if w == nil {
		return nil
	}

	w.mu.Lock()
	entries := w.filteredEntriesLocked(now)
	w.mu.Unlock()

	samples := make([]time.Duration, 0, len(entries))
	for _, entry := range entries {
		samples = append(samples, entry.Latency)
	}

	return samples
}

func (w *SlidingWindow) Snapshot(now time.Time) WindowSnapshot {
	if w == nil {
		return WindowSnapshot{}
	}

	w.mu.Lock()
	entries := w.filteredEntriesLocked(now)
	w.mu.Unlock()

	durations := make([]time.Duration, 0, len(entries))
	errorCount := 0
	for _, entry := range entries {
		durations = append(durations, entry.Latency)
		if entry.IsError {
			errorCount++
		}
	}

	sort.Slice(durations, func(i, j int) bool {
		return durations[i] < durations[j]
	})

	snapshot := WindowSnapshot{
		Count:      len(entries),
		ErrorCount: errorCount,
		P50:        percentileFromSorted(durations, 50),
		P95:        percentileFromSorted(durations, 95),
		P99:        percentileFromSorted(durations, 99),
	}

	if snapshot.Count > 0 {
		snapshot.ReqPerSec = float64(snapshot.Count) / w.maxAge.Seconds()
		snapshot.ErrorRate = float64(errorCount) / float64(snapshot.Count)
	}

	return snapshot
}

func (w *SlidingWindow) filteredEntriesLocked(now time.Time) []LatencyEntry {
	if w.count == 0 || len(w.entries) == 0 {
		return nil
	}

	filtered := make([]LatencyEntry, 0, w.count)
	for i := 0; i < w.count; i++ {
		index := i
		if len(w.entries) == w.maxEntries {
			index = (w.start + i) % w.maxEntries
		}

		entry := w.entries[index]
		if now.Sub(entry.Timestamp) <= w.maxAge {
			filtered = append(filtered, entry)
		}
	}

	if len(filtered) == w.count {
		return filtered
	}

	w.entries = append(make([]LatencyEntry, 0, w.maxEntries), filtered...)
	w.start = 0
	w.count = len(filtered)
	return filtered
}

func shouldEmitInfoLogs(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "debug", "info":
		return true
	default:
		return false
	}
}
