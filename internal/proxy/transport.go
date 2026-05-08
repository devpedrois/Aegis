package proxy

import (
	"net/http"
	"time"

	"github.com/user/aegis/internal/metrics"
	"github.com/user/aegis/internal/pool"
)

type InstrumentedTransport struct {
	base      http.RoundTripper
	collector *metrics.MetricsCollector
	backend   *pool.Backend
}

func NewInstrumentedTransport(base http.RoundTripper, collector *metrics.MetricsCollector, backend *pool.Backend) *InstrumentedTransport {
	if base == nil {
		base = http.DefaultTransport
	}

	return &InstrumentedTransport{
		base:      base,
		collector: collector,
		backend:   backend,
	}
}

func NewInstrumentedTransportFactory(collector *metrics.MetricsCollector) pool.TransportFactory {
	return func(base *http.Transport, backend *pool.Backend) http.RoundTripper {
		return NewInstrumentedTransport(base, collector, backend)
	}
}

func (t *InstrumentedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()

	if t.backend != nil {
		t.backend.ActiveRequests.Add(1)
		defer t.backend.ActiveRequests.Add(-1)
	}

	resp, err := t.base.RoundTrip(req)
	latency := time.Since(start)

	if err != nil {
		if t.backend != nil {
			t.backend.TotalErrors.Add(1)
			if t.backend.CircuitBreaker != nil {
				// [SECURITY] Upstream dial and transport errors count as backend failures because repeated connection faults indicate an unsafe target.
				t.backend.CircuitBreaker.RecordFailure()
			}
		}

		if t.collector != nil && t.backend != nil && t.backend.URL != nil {
			t.collector.RecordLatency(t.backend.URL.String(), latency, true)
		}

		return resp, err
	}

	isError := resp.StatusCode >= http.StatusInternalServerError
	if t.backend != nil {
		if isError {
			t.backend.TotalErrors.Add(1)
			if t.backend.CircuitBreaker != nil {
				// [SECURITY] Upstream 5xx responses trip failure accounting so broken nodes can be isolated before they cause cascade failure.
				t.backend.CircuitBreaker.RecordFailure()
			}
		} else if t.backend.CircuitBreaker != nil {
			t.backend.CircuitBreaker.RecordSuccess()
		}

		t.backend.TotalRequests.Add(1)
	}

	if t.collector != nil && t.backend != nil && t.backend.URL != nil {
		t.collector.RecordLatency(t.backend.URL.String(), latency, isError)
	}

	return resp, err
}
