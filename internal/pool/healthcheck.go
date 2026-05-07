package pool

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/user/aegis/internal/config"
)

func StartHealthChecks(ctx context.Context, pool *BackendPool, cfg config.HealthCheckConfig) {
	for _, backend := range pool.GetAll() {
		go runHealthCheck(ctx, backend, pool, cfg)
	}
}

func runHealthCheck(ctx context.Context, backend *Backend, pool *BackendPool, cfg config.HealthCheckConfig) {
	client := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: backend.Proxy.Transport,
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// [SECURITY] Health-check workers must exit on context cancellation to avoid goroutine leaks under shutdown pressure.
			return
		case <-ticker.C:
			runHealthCheckTick(ctx, client, backend, pool, cfg)
		}
	}
}

func runHealthCheckTick(ctx context.Context, client *http.Client, backend *Backend, pool *BackendPool, cfg config.HealthCheckConfig) {
	checkURL := healthCheckURL(backend.URL, cfg.Path)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
	if err != nil {
		log.Printf("DEBUG Health check %s: request build error: %v", backend.URL.String(), err)
		return
	}

	// [SECURITY] Health checks bypass request middleware because they are trusted control-plane probes, not client-originated traffic.
	response, err := client.Do(request)
	if err != nil {
		recordHealthCheckFailure(backend, pool, cfg)
		log.Printf("DEBUG Health check %s: error", backend.URL.String())
		return
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		recordHealthCheckFailure(backend, pool, cfg)
		log.Printf("DEBUG Health check %s: status=%d", backend.URL.String(), response.StatusCode)
		return
	}

	recordHealthCheckSuccess(backend, pool, cfg)
	log.Printf("DEBUG Health check %s: ok", backend.URL.String())
}

func recordHealthCheckSuccess(backend *Backend, pool *BackendPool, cfg config.HealthCheckConfig) {
	backend.ConsecFails.Store(0)
	successes := backend.ConsecSuccesses.Add(1)

	if !backend.Healthy.Load() && successes >= int32(cfg.HealthyThreshold) {
		pool.MarkHealthy(backend.URL.String())
		return
	}

	if !backend.Healthy.Load() {
		return
	}

	currentWarmup := backend.WarmupLevel.Load()
	if currentWarmup >= 3 {
		return
	}

	if successes%3 == 0 {
		nextWarmup := currentWarmup + 1
		if nextWarmup > 3 {
			nextWarmup = 3
		}

		backend.WarmupLevel.Store(nextWarmup)
		log.Printf("INFO Warmup %s: %d%%", backend.URL.String(), (nextWarmup+1)*25)
	}
}

func recordHealthCheckFailure(backend *Backend, pool *BackendPool, cfg config.HealthCheckConfig) {
	backend.ConsecSuccesses.Store(0)
	failures := backend.ConsecFails.Add(1)
	if failures >= int32(cfg.UnhealthyThreshold) && backend.Healthy.Load() {
		pool.MarkUnhealthy(backend.URL.String())
	}
}

func healthCheckURL(targetURL *url.URL, path string) string {
	clone := cloneURL(targetURL)
	clone.Path = path
	clone.RawPath = ""
	return clone.String()
}
