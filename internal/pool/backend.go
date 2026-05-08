package pool

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"

	"github.com/user/aegis/internal/circuit"
	"github.com/user/aegis/internal/security"
)

type DirectorFactory func(targetURL *url.URL, hostHeader string) func(*http.Request)
type TransportFactory func(base *http.Transport, backend *Backend) http.RoundTripper

type Backend struct {
	URL    *url.URL
	Weight int
	// Backend mutable state uses atomics because requests and health checks run concurrently.
	Healthy atomic.Bool
	// Health-check counters stay atomic to avoid races during concurrent state transitions.
	ConsecFails     atomic.Int32
	ConsecSuccesses atomic.Int32
	WarmupLevel     atomic.Int32
	// Runtime counters stay atomic because request instrumentation and health monitoring execute concurrently.
	LatencyP50     atomic.Int64
	LatencyP95     atomic.Int64
	LatencyP99     atomic.Int64
	ActiveRequests atomic.Int64
	TotalRequests  atomic.Int64
	TotalErrors    atomic.Int64
	CircuitBreaker *circuit.CircuitBreaker
	// [SECURITY] Health checks use a dedicated uninstrumented transport so trusted control-plane probes cannot poison client telemetry.
	HealthCheckTransport http.RoundTripper
	Proxy                *httputil.ReverseProxy
}

var errRejectedUpstreamResponse = errors.New("rejected upstream response")

func NewBackend(targetURL *url.URL, weight int, pinnedAddress string, serverName string, hostHeader string, directorFactory DirectorFactory, transportFactory TransportFactory) (*Backend, error) {
	if targetURL == nil {
		return nil, fmt.Errorf("backend URL must not be nil")
	}

	if weight <= 0 {
		return nil, fmt.Errorf("backend weight must be greater than zero")
	}

	if pinnedAddress == "" {
		return nil, fmt.Errorf("backend pinned address must not be empty")
	}

	if directorFactory == nil {
		return nil, fmt.Errorf("backend director factory must not be nil")
	}

	director := directorFactory(targetURL, hostHeader)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		// [SECURITY] Upstream connections stay pinned to the validated startup address to reduce DNS rebinding risk.
		return (&net.Dialer{}).DialContext(ctx, network, pinnedAddress)
	}

	if targetURL.Scheme == "https" && serverName != "" {
		transport.TLSClientConfig = &tls.Config{
			ServerName: serverName,
			MinVersion: tls.VersionTLS12,
		}
	}

	backend := &Backend{
		URL:                  cloneURL(targetURL),
		Weight:               weight,
		HealthCheckTransport: transport,
	}

	roundTripper := http.RoundTripper(transport)
	if transportFactory != nil {
		roundTripper = transportFactory(transport, backend)
	}

	backend.Proxy = &httputil.ReverseProxy{
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			// [SECURITY] The trusted peer address is copied from the inbound request so edge-derived identity remains available during header rewriting.
			proxyRequest.Out.RemoteAddr = proxyRequest.In.RemoteAddr
			director(proxyRequest.Out)
			if ip := security.ExtractIP(proxyRequest.In); ip != "" {
				// [SECURITY] The forwarded client IP is asserted again from the inbound connection to prevent rewrite-path header loss or spoofing.
				proxyRequest.Out.Header.Set("X-Forwarded-For", ip)
			}
		},
		Transport: roundTripper,
		ModifyResponse: func(resp *http.Response) error {
			if resp == nil {
				return nil
			}

			wrapUpstreamBodyForTrailerStripping(resp)
			stripUpstreamTrailers(resp)

			if resp.StatusCode < http.StatusInternalServerError {
				return nil
			}

			if resp.Body != nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}

			// [SECURITY] Upstream 5xx payloads are discarded so backend stack traces and banners cannot be reflected to clients.
			return errRejectedUpstreamResponse
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				// [SECURITY] Oversized client bodies are rejected as 413 at the edge so abuse is attributed correctly and not misreported as an upstream fault.
				writeProxyRequestTooLarge(w)
				return
			}

			slog.Error("proxy upstream failure",
				"backend", targetURL.String(),
				"error", err.Error(),
			)
			writeProxyBadGateway(w)
		},
	}
	backend.Healthy.Store(true)
	backend.WarmupLevel.Store(3)

	return backend, nil
}

func cloneURL(source *url.URL) *url.URL {
	if source == nil {
		return nil
	}

	clone := *source
	return &clone
}

func writeProxyBadGateway(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	_, _ = w.Write([]byte(`{"error":"bad gateway"}`))
}

func writeProxyRequestTooLarge(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusRequestEntityTooLarge)
	_, _ = w.Write([]byte(`{"error":"request body too large"}`))
}

func stripUpstreamTrailers(resp *http.Response) {
	if resp == nil {
		return
	}

	// [SECURITY] Upstream response trailers are stripped entirely because they can carry delayed metadata that bypasses header sanitization at the edge.
	resp.Header.Del("Trailer")
	resp.Trailer = nil
}

func wrapUpstreamBodyForTrailerStripping(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}

	resp.Body = &trailerStrippingReadCloser{
		ReadCloser: resp.Body,
		resp:       resp,
	}
}

type trailerStrippingReadCloser struct {
	io.ReadCloser
	resp *http.Response
}

func (r *trailerStrippingReadCloser) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF {
		r.stripTrailers()
	}

	return n, err
}

func (r *trailerStrippingReadCloser) Close() error {
	r.stripTrailers()
	if r.ReadCloser == nil {
		return nil
	}

	return r.ReadCloser.Close()
}

func (r *trailerStrippingReadCloser) stripTrailers() {
	if r == nil || r.resp == nil {
		return
	}

	// [SECURITY] Trailers are cleared again at EOF because upstream clients populate them only after the body has been fully read.
	r.resp.Header.Del("Trailer")
	r.resp.Trailer = nil
}
