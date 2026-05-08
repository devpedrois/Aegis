package ratelimit

import (
	"context"
	"log/slog"
	"time"

	"github.com/user/aegis/internal/config"
	"github.com/user/aegis/internal/metrics"
)

func RunAdaptive(ctx context.Context, collector *metrics.MetricsCollector, limiter *RateLimiter, cfg config.AdaptiveConfig) {
	if ctx == nil || collector == nil || limiter == nil || cfg.EvaluationInterval <= 0 {
		return
	}

	go func() {
		ticker := time.NewTicker(cfg.EvaluationInterval)
		defer ticker.Stop()

		lastActionWasReduction := false
		collector.SetControlState(limiter.CurrentRate(), limiter.BlockedCount(), "normal")

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snapshot := collector.Snapshot()
				aggregatedP95, ok := aggregatedHealthyClosedP95(collector, snapshot)
				currentRate := limiter.CurrentRate()
				state := "normal"

				if !ok {
					collector.SetControlState(currentRate, limiter.BlockedCount(), state)
					continue
				}

				thresholdDuration := time.Duration(cfg.LatencyThresholdMS) * time.Millisecond
				if aggregatedP95 > thresholdDuration {
					state = "reducing"
					if !lastActionWasReduction {
						newRate := maxRate(currentRate*cfg.ReductionFactor, cfg.MinRate)
						limiter.SetGlobalRate(newRate)
						currentRate = limiter.CurrentRate()
						slog.Warn("Adaptive: reducing rate", "new_rate", currentRate, "p95_ms", aggregatedP95.Milliseconds())
						lastActionWasReduction = true
					}
				} else if aggregatedP95 < (thresholdDuration * 7 / 10) {
					state = "recovering"
					newRate := minRate(currentRate*cfg.RecoveryFactor, cfg.MaxRate)
					limiter.SetGlobalRate(newRate)
					currentRate = limiter.CurrentRate()
					slog.Info("Adaptive: recovering rate", "new_rate", currentRate, "p95_ms", aggregatedP95.Milliseconds())
					lastActionWasReduction = false
				} else {
					lastActionWasReduction = false
				}

				collector.SetControlState(currentRate, limiter.BlockedCount(), state)
			}
		}
	}()
}

func aggregatedHealthyClosedP95(collector *metrics.MetricsCollector, snapshot metrics.MetricsSnapshot) (time.Duration, bool) {
	candidates := make([]time.Duration, 0, len(snapshot.BackendStats))
	for _, backend := range snapshot.BackendStats {
		if !backend.Healthy || backend.CircuitState != "closed" {
			continue
		}

		samples := collector.LatencySamples(backend.URL)
		for _, sample := range samples {
			if sample <= 0 {
				continue
			}

			candidates = append(candidates, sample)
		}
	}

	if len(candidates) == 0 {
		return 0, false
	}

	// [SECURITY] Adaptive decisions are derived from observed latency samples only, so thin outliers cannot dominate the global rate unfairly.
	return metrics.Percentile(candidates, 95), true
}

func maxRate(current float64, minimum float64) float64 {
	if current < minimum {
		return minimum
	}

	return current
}

func minRate(current float64, maximum float64) float64 {
	if current > maximum {
		return maximum
	}

	return current
}
