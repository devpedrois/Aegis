package pool

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
)

type DirectorFactory func(targetURL *url.URL, hostHeader string) func(*http.Request)

type Backend struct {
	URL    *url.URL
	Weight int
	// [SECURITY] Backend mutable state uses atomics because requests and health checks run concurrently.
	Healthy atomic.Bool
	// [SECURITY] Health-check counters stay atomic to avoid races during concurrent state transitions.
	ConsecFails     atomic.Int32
	ConsecSuccesses atomic.Int32
	WarmupLevel     atomic.Int32
	Proxy           *httputil.ReverseProxy
}

func NewBackend(targetURL *url.URL, weight int, pinnedAddress string, serverName string, hostHeader string, directorFactory DirectorFactory) (*Backend, error) {
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
		URL:    cloneURL(targetURL),
		Weight: weight,
		Proxy: &httputil.ReverseProxy{
			Rewrite: func(proxyRequest *httputil.ProxyRequest) {
				director(proxyRequest.Out)
			},
			Transport: transport,
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
