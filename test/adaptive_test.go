package test

import (
	"context"
	"testing"
	"time"

	cfgpkg "github.com/user/aegis/internal/config"
	metricspkg "github.com/user/aegis/internal/metrics"
	ratelimitpkg "github.com/user/aegis/internal/ratelimit"
)

func TestAdaptiveRunReducesRateWhenP95ExceedsThreshold(t *testing.T) {
	t.Parallel()

	collector, limiter := newAdaptiveHarness(t, 100)
	recordBackendLatencies(collector, 900*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ratelimitpkg.RunAdaptive(ctx, collector, limiter, cfgpkg.AdaptiveConfig{
		EvaluationInterval: 10 * time.Millisecond,
		LatencyThresholdMS: 500,
		ReductionFactor:    0.8,
		RecoveryFactor:     1.1,
		MinRate:            10,
		MaxRate:            500,
	})

	requireEventually(t, time.Second, func() bool {
		return limiter.CurrentRate() == 80
	})

	requireEventually(t, time.Second, func() bool {
		snapshot := collector.Snapshot()
		return snapshot.CurrentRate == 80 && snapshot.AdaptiveState == "reducing"
	})
}

func TestAdaptiveRunRecoversRateWhenP95DropsBelowRecoveryWindow(t *testing.T) {
	t.Parallel()

	collector, limiter := newAdaptiveHarness(t, 100)
	recordBackendLatencies(collector, 100*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ratelimitpkg.RunAdaptive(ctx, collector, limiter, cfgpkg.AdaptiveConfig{
		EvaluationInterval: 10 * time.Millisecond,
		LatencyThresholdMS: 500,
		ReductionFactor:    0.8,
		RecoveryFactor:     1.1,
		MinRate:            10,
		MaxRate:            500,
	})

	requireEventually(t, time.Second, func() bool {
		return limiter.CurrentRate() > 100
	})

	requireEventually(t, time.Second, func() bool {
		return collector.Snapshot().AdaptiveState == "recovering"
	})
}

func TestAdaptiveRunUsesHysteresisAcrossConsecutiveHighLatencyCycles(t *testing.T) {
	t.Parallel()

	collector, limiter := newAdaptiveHarness(t, 100)
	recordBackendLatencies(collector, 900*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ratelimitpkg.RunAdaptive(ctx, collector, limiter, cfgpkg.AdaptiveConfig{
		EvaluationInterval: 10 * time.Millisecond,
		LatencyThresholdMS: 500,
		ReductionFactor:    0.8,
		RecoveryFactor:     1.1,
		MinRate:            10,
		MaxRate:            500,
	})

	time.Sleep(80 * time.Millisecond)

	if got := limiter.CurrentRate(); got != 80 {
		t.Fatalf("CurrentRate() after repeated high latency = %v, want single reduction to %v", got, 80.0)
	}
}

func TestAdaptiveRunClampsRateToConfiguredBounds(t *testing.T) {
	t.Parallel()

	highCollector, highLimiter := newAdaptiveHarness(t, 11)
	recordBackendLatencies(highCollector, 900*time.Millisecond)

	highCtx, highCancel := context.WithCancel(context.Background())
	defer highCancel()

	ratelimitpkg.RunAdaptive(highCtx, highCollector, highLimiter, cfgpkg.AdaptiveConfig{
		EvaluationInterval: 10 * time.Millisecond,
		LatencyThresholdMS: 500,
		ReductionFactor:    0.1,
		RecoveryFactor:     1.1,
		MinRate:            10,
		MaxRate:            500,
	})

	requireEventually(t, time.Second, func() bool {
		return highLimiter.CurrentRate() == 10
	})

	lowCollector, lowLimiter := newAdaptiveHarness(t, 490)
	recordBackendLatencies(lowCollector, 100*time.Millisecond)

	lowCtx, lowCancel := context.WithCancel(context.Background())
	defer lowCancel()

	ratelimitpkg.RunAdaptive(lowCtx, lowCollector, lowLimiter, cfgpkg.AdaptiveConfig{
		EvaluationInterval: 10 * time.Millisecond,
		LatencyThresholdMS: 500,
		ReductionFactor:    0.8,
		RecoveryFactor:     1.5,
		MinRate:            10,
		MaxRate:            500,
	})

	requireEventually(t, time.Second, func() bool {
		return lowLimiter.CurrentRate() == 500
	})
}

func TestAdaptiveRunIgnoresUnhealthyAndOpenCircuitBackends(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 1},
		{name: "b", weight: 1},
	})

	pool.GetAll()[0].Healthy.Store(false)
	pool.GetAll()[1].CircuitBreaker = newTestBreaker(time.Second, 1, 2, 2)
	pool.GetAll()[1].CircuitBreaker.RecordFailure()

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())
	recordBackendLatenciesForURL(collector, pool.GetAll()[0].URL.String(), 900*time.Millisecond)
	recordBackendLatenciesForURL(collector, pool.GetAll()[1].URL.String(), 900*time.Millisecond)

	limiter := ratelimitpkg.NewRateLimiter(100, 100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ratelimitpkg.RunAdaptive(ctx, collector, limiter, cfgpkg.AdaptiveConfig{
		EvaluationInterval: 10 * time.Millisecond,
		LatencyThresholdMS: 500,
		ReductionFactor:    0.8,
		RecoveryFactor:     1.1,
		MinRate:            10,
		MaxRate:            500,
	})

	time.Sleep(40 * time.Millisecond)

	if got := limiter.CurrentRate(); got != 100 {
		t.Fatalf("CurrentRate() = %v, want unchanged 100 when no healthy closed backends exist", got)
	}
}

func TestAdaptiveRunUsesAggregatedObservedSamplesAcrossBackends(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 1},
		{name: "b", weight: 1},
	})
	for _, backend := range pool.GetAll() {
		backend.CircuitBreaker = newTestBreaker(time.Second, 5, 3, 3)
	}

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())

	for i := 0; i < 100; i++ {
		recordBackendLatenciesForURL(collector, pool.GetAll()[0].URL.String(), 100*time.Millisecond)
	}
	recordBackendLatenciesForURL(collector, pool.GetAll()[1].URL.String(), 900*time.Millisecond)

	limiter := ratelimitpkg.NewRateLimiter(100, 100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ratelimitpkg.RunAdaptive(ctx, collector, limiter, cfgpkg.AdaptiveConfig{
		EvaluationInterval: 10 * time.Millisecond,
		LatencyThresholdMS: 500,
		ReductionFactor:    0.8,
		RecoveryFactor:     1.1,
		MinRate:            10,
		MaxRate:            500,
	})

	time.Sleep(40 * time.Millisecond)

	if got := limiter.CurrentRate(); got <= 100 {
		t.Fatalf("CurrentRate() = %v, want recovery or no reduction because aggregated sample P95 stays low", got)
	}
}

func TestAdaptiveRunDoesNotRecoverWithoutObservedLatency(t *testing.T) {
	t.Parallel()

	collector, limiter := newAdaptiveHarness(t, 100)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ratelimitpkg.RunAdaptive(ctx, collector, limiter, cfgpkg.AdaptiveConfig{
		EvaluationInterval: 10 * time.Millisecond,
		LatencyThresholdMS: 500,
		ReductionFactor:    0.8,
		RecoveryFactor:     1.1,
		MinRate:            10,
		MaxRate:            500,
	})

	time.Sleep(40 * time.Millisecond)

	if got := limiter.CurrentRate(); got != 100 {
		t.Fatalf("CurrentRate() = %v, want unchanged 100 without observed latency", got)
	}
}

func newAdaptiveHarness(t *testing.T, initialRate float64) (*metricspkg.MetricsCollector, *ratelimitpkg.RateLimiter) {
	t.Helper()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	pool.GetAll()[0].CircuitBreaker = newTestBreaker(time.Second, 5, 3, 3)

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())

	limiter := ratelimitpkg.NewRateLimiter(initialRate, 100)
	return collector, limiter
}

func recordBackendLatencies(collector *metricspkg.MetricsCollector, latency time.Duration) {
	recordBackendLatenciesForURL(collector, collector.Snapshot().BackendStats[0].URL, latency)
}

func recordBackendLatenciesForURL(collector *metricspkg.MetricsCollector, backendURL string, latencies ...time.Duration) {
	for _, latency := range latencies {
		collector.RecordLatency(backendURL, latency, false)
	}
}
