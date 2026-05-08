package circuit

import (
	"log/slog"
	"sync"
	"time"

	"github.com/user/aegis/internal/config"
)

type CircuitBreaker struct {
	state            State
	failureCount     int
	successCount     int
	halfOpenRequests int
	failureThreshold int
	successThreshold int
	openTimeout      time.Duration
	halfOpenMax      int
	lastStateChange  time.Time
	backendName      string
	// Each backend breaker uses a dedicated mutex so state transitions stay coherent under concurrent failure pressure.
	mu sync.Mutex
}

func NewCircuitBreaker(cfg config.CircuitBreakerConfig) *CircuitBreaker {
	now := time.Now()
	return &CircuitBreaker{
		state:            Closed,
		failureThreshold: cfg.FailureThreshold,
		successThreshold: cfg.SuccessThreshold,
		openTimeout:      cfg.OpenTimeout,
		halfOpenMax:      cfg.HalfOpenMaxRequests,
		lastStateChange:  now,
	}
}

func (cb *CircuitBreaker) SetBackendName(name string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.backendName = name
}

func (cb *CircuitBreaker) AllowRequest() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case Closed:
		return true
	case Open:
		if time.Since(cb.lastStateChange) <= cb.openTimeout {
			return false
		}

		cb.state = HalfOpen
		cb.successCount = 0
		cb.failureCount = 0
		cb.halfOpenRequests = 1
		cb.lastStateChange = time.Now()
		slog.Warn("Circuit half-open", "backend", cb.backendName)
		return true
	case HalfOpen:
		if cb.halfOpenRequests >= cb.halfOpenMax {
			return false
		}

		cb.halfOpenRequests++
		return true
	default:
		return false
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case Closed:
		cb.failureCount = 0
	case HalfOpen:
		cb.successCount++
		if cb.successCount >= cb.successThreshold {
			cb.resetLocked(Closed)
			slog.Info("Circuit closed", "backend", cb.backendName)
		}
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case Closed:
		cb.failureCount++
		if cb.failureCount >= cb.failureThreshold {
			cb.tripOpenLocked()
			slog.Error("Circuit open", "backend", cb.backendName, "failures", cb.failureCount)
		}
	case HalfOpen:
		cb.tripOpenLocked()
		slog.Error("Circuit re-opened", "backend", cb.backendName)
	}
}

func (cb *CircuitBreaker) State() State {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	return cb.state
}

func (cb *CircuitBreaker) tripOpenLocked() {
	cb.state = Open
	cb.successCount = 0
	cb.halfOpenRequests = 0
	cb.lastStateChange = time.Now()
}

func (cb *CircuitBreaker) resetLocked(state State) {
	cb.state = state
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenRequests = 0
	cb.lastStateChange = time.Now()
}
