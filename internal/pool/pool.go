package pool

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/user/aegis/internal/config"
)

type BackendPool struct {
	backends []*Backend
	// The backend slice is guarded by RWMutex because request routing and health checks read and mutate shared membership state.
	mu sync.RWMutex
	// The request index uses atomic increments because selection happens concurrently for every request.
	index atomic.Uint64
}

func NewPool(configs []config.BackendConfig, directorFactory DirectorFactory, transportFactories ...TransportFactory) (*BackendPool, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("at least one backend is required")
	}

	var transportFactory TransportFactory
	if len(transportFactories) > 0 {
		transportFactory = transportFactories[0]
	}

	backends := make([]*Backend, 0, len(configs))
	for _, backendConfig := range configs {
		targetURL, err := url.Parse(backendConfig.URL)
		if err != nil {
			return nil, fmt.Errorf("parse backend URL %q: %w", backendConfig.URL, err)
		}

		backend, err := NewBackend(
			targetURL,
			backendConfig.Weight,
			backendConfig.PinnedAddress,
			backendConfig.ServerName,
			backendConfig.OriginalHost,
			directorFactory,
			transportFactory,
		)
		if err != nil {
			return nil, fmt.Errorf("create backend %q: %w", backendConfig.URL, err)
		}

		backends = append(backends, backend)
	}

	return &BackendPool{backends: backends}, nil
}

func (p *BackendPool) NextHealthy() (*Backend, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	weightedBackends := buildWeightedSchedule(p.backends)

	if len(weightedBackends) == 0 {
		return nil, fmt.Errorf("no healthy backends available")
	}

	start := int((p.index.Add(1) - 1) % uint64(len(weightedBackends)))
	for i := 0; i < len(weightedBackends); i++ {
		selected := weightedBackends[(start+i)%len(weightedBackends)]
		if selected.Healthy.Load() && allowBackendRequest(selected) {
			return selected, nil
		}
	}

	return nil, fmt.Errorf("no healthy backends available")
}

func buildWeightedSchedule(backends []*Backend) []*Backend {
	maxWeight := 0
	effectiveWeights := make([]int, len(backends))
	for i, backend := range backends {
		if !backend.Healthy.Load() {
			continue
		}

		effectiveWeight := backend.Weight * warmupMultiplier(backend.WarmupLevel.Load())
		effectiveWeights[i] = effectiveWeight
		if effectiveWeight > maxWeight {
			maxWeight = effectiveWeight
		}
	}

	schedule := make([]*Backend, 0, maxWeight*len(backends))
	for round := 0; round < maxWeight; round++ {
		for i, backend := range backends {
			if !backend.Healthy.Load() {
				continue
			}

			if effectiveWeights[i] > round {
				schedule = append(schedule, backend)
			}
		}
	}

	return schedule
}

func (p *BackendPool) MarkUnhealthy(rawURL string) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, backend := range p.backends {
		if backend.URL.String() != rawURL {
			continue
		}

		if backend.Healthy.Swap(false) {
			backend.ConsecSuccesses.Store(0)
			log.Printf("WARN Backend unhealthy: %s", rawURL)
		}

		return
	}
}

func (p *BackendPool) MarkHealthy(rawURL string) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, backend := range p.backends {
		if backend.URL.String() != rawURL {
			continue
		}

		backend.ConsecFails.Store(0)
		backend.ConsecSuccesses.Store(0)
		backend.WarmupLevel.Store(0)
		backend.Healthy.Store(true)
		log.Printf("INFO Backend recovering: %s (warmup 25%%)", rawURL)
		return
	}
}

func (p *BackendPool) GetAll() []*Backend {
	p.mu.RLock()
	defer p.mu.RUnlock()

	backends := make([]*Backend, len(p.backends))
	copy(backends, p.backends)
	return backends
}

func warmupMultiplier(level int32) int {
	switch {
	case level <= 0:
		return 1
	case level == 1:
		return 2
	case level == 2:
		return 3
	default:
		return 4
	}
}

func allowBackendRequest(backend *Backend) bool {
	if backend == nil {
		return false
	}

	if backend.CircuitBreaker == nil {
		return true
	}

	// [SECURITY] Routing delegates admission to the breaker so open circuits can recover through bounded half-open probes instead of remaining permanently isolated.
	return backend.CircuitBreaker.AllowRequest()
}

var _ http.Handler
