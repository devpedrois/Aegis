package test

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	circuitpkg "github.com/user/aegis/internal/circuit"
	cfgpkg "github.com/user/aegis/internal/config"
	metricspkg "github.com/user/aegis/internal/metrics"
	proxypkg "github.com/user/aegis/internal/proxy"
)

func TestCircuitStateString(t *testing.T) {
	t.Parallel()

	cases := map[circuitpkg.State]string{
		circuitpkg.Closed:   "closed",
		circuitpkg.Open:     "open",
		circuitpkg.HalfOpen: "half-open",
	}

	for state, want := range cases {
		if got := state.String(); got != want {
			t.Fatalf("State(%d).String() = %q, want %q", state, got, want)
		}
	}
}

func TestCircuitBreakerClosedTransitionsToOpenAfterThresholdFailures(t *testing.T) {
	t.Parallel()

	breaker := newTestBreaker(20*time.Millisecond, 3, 2, 2)

	breaker.RecordFailure()
	breaker.RecordFailure()
	if got := breaker.State(); got != circuitpkg.Closed {
		t.Fatalf("State() after 2 failures = %v, want %v", got, circuitpkg.Closed)
	}

	breaker.RecordFailure()
	if got := breaker.State(); got != circuitpkg.Open {
		t.Fatalf("State() after threshold failures = %v, want %v", got, circuitpkg.Open)
	}
}

func TestCircuitBreakerOpenRejectsRequestsBeforeTimeout(t *testing.T) {
	t.Parallel()

	breaker := newTestBreaker(50*time.Millisecond, 1, 2, 2)
	breaker.RecordFailure()

	if breaker.AllowRequest() {
		t.Fatal("AllowRequest() = true, want false while open before timeout")
	}
}

func TestCircuitBreakerOpenAllowsHalfOpenProbeAfterTimeout(t *testing.T) {
	t.Parallel()

	breaker := newTestBreaker(20*time.Millisecond, 1, 2, 2)
	breaker.RecordFailure()

	time.Sleep(25 * time.Millisecond)

	if !breaker.AllowRequest() {
		t.Fatal("AllowRequest() = false, want true after open timeout")
	}

	if got := breaker.State(); got != circuitpkg.HalfOpen {
		t.Fatalf("State() after timeout probe = %v, want %v", got, circuitpkg.HalfOpen)
	}
}

func TestCircuitBreakerHalfOpenClosesAfterSuccessThreshold(t *testing.T) {
	t.Parallel()

	breaker := newTestBreaker(20*time.Millisecond, 1, 2, 3)
	breaker.RecordFailure()

	time.Sleep(25 * time.Millisecond)
	if !breaker.AllowRequest() {
		t.Fatal("AllowRequest() = false, want first half-open request")
	}
	breaker.RecordSuccess()

	if !breaker.AllowRequest() {
		t.Fatal("AllowRequest() = false, want second half-open request")
	}
	breaker.RecordSuccess()

	if got := breaker.State(); got != circuitpkg.Closed {
		t.Fatalf("State() after success threshold = %v, want %v", got, circuitpkg.Closed)
	}
}

func TestCircuitBreakerHalfOpenFailureReopensCircuit(t *testing.T) {
	t.Parallel()

	breaker := newTestBreaker(20*time.Millisecond, 1, 2, 2)
	breaker.RecordFailure()

	time.Sleep(25 * time.Millisecond)
	if !breaker.AllowRequest() {
		t.Fatal("AllowRequest() = false, want half-open test request")
	}

	breaker.RecordFailure()

	if got := breaker.State(); got != circuitpkg.Open {
		t.Fatalf("State() after half-open failure = %v, want %v", got, circuitpkg.Open)
	}
}

func TestCircuitBreakerConcurrentAccessDoesNotRace(t *testing.T) {
	t.Parallel()

	breaker := newTestBreaker(5*time.Millisecond, 2, 2, 3)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			if breaker.AllowRequest() {
				if index%3 == 0 {
					breaker.RecordFailure()
					return
				}

				breaker.RecordSuccess()
			}
		}(i)
	}

	wg.Wait()

	state := breaker.State()
	if state != circuitpkg.Closed && state != circuitpkg.Open && state != circuitpkg.HalfOpen {
		t.Fatalf("State() = %v, want valid circuit state", state)
	}
}

func TestBackendPoolSkipsOpenCircuitBackends(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{
		{name: "a", weight: 1},
		{name: "b", weight: 1},
	})

	backendA := pool.GetAll()[0]
	backendA.CircuitBreaker = newTestBreaker(time.Second, 1, 2, 2)
	backendA.CircuitBreaker.RecordFailure()

	for i := 0; i < 3; i++ {
		backend, err := pool.NextHealthy()
		if err != nil {
			t.Fatalf("NextHealthy() error = %v", err)
		}

		if got := backend.URL.Host; got != "b.example" {
			t.Fatalf("NextHealthy() backend = %q, want %q", got, "b.example")
		}
	}
}

func TestBackendPoolAllowsHalfOpenProbeAfterOpenTimeout(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	backend := pool.GetAll()[0]
	backend.CircuitBreaker = newTestBreaker(20*time.Millisecond, 1, 2, 1)
	backend.CircuitBreaker.RecordFailure()

	if _, err := pool.NextHealthy(); err == nil {
		t.Fatal("NextHealthy() error = nil, want no healthy backends while circuit is still open")
	}

	time.Sleep(25 * time.Millisecond)

	selected, err := pool.NextHealthy()
	if err != nil {
		t.Fatalf("NextHealthy() after timeout error = %v", err)
	}

	if selected != backend {
		t.Fatal("NextHealthy() returned unexpected backend during half-open probe")
	}

	if _, err := pool.NextHealthy(); err == nil {
		t.Fatal("NextHealthy() error = nil, want probe limit enforced in half-open state")
	}
}

func TestInstrumentedTransportRecordsServerErrorsAsCircuitFailures(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	backend := pool.GetAll()[0]
	backend.CircuitBreaker = newTestBreaker(time.Second, 2, 2, 2)

	collector := metricspkg.NewMetricsCollector(time.Minute, 1000)
	collector.SetBackends(pool.GetAll())

	transport := proxypkg.NewInstrumentedTransport(roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       http.NoBody,
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}), collector, backend)

	req, err := http.NewRequest(http.MethodGet, "http://a.example", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	for i := 0; i < 2; i++ {
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip() error = %v", err)
		}
		resp.Body.Close()
	}

	if got := backend.CircuitBreaker.State(); got != circuitpkg.Open {
		t.Fatalf("CircuitBreaker.State() = %v, want %v", got, circuitpkg.Open)
	}
}

func TestInstrumentedTransportRecordsRoundTripErrorsAsCircuitFailures(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	backend := pool.GetAll()[0]
	backend.CircuitBreaker = newTestBreaker(time.Second, 1, 2, 2)

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
		t.Fatal("RoundTrip() error = nil, want dial failure")
	}

	if got := backend.CircuitBreaker.State(); got != circuitpkg.Open {
		t.Fatalf("CircuitBreaker.State() = %v, want %v", got, circuitpkg.Open)
	}
}

func newTestBreaker(openTimeout time.Duration, failureThreshold, successThreshold, halfOpenMax int) *circuitpkg.CircuitBreaker {
	return circuitpkg.NewCircuitBreaker(cfgpkg.CircuitBreakerConfig{
		FailureThreshold:    failureThreshold,
		SuccessThreshold:    successThreshold,
		OpenTimeout:         openTimeout,
		HalfOpenMaxRequests: halfOpenMax,
	})
}
