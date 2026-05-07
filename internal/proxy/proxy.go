package proxy

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

type Target struct {
	URL         *url.URL
	HostHeader  string
	DialAddress string
	ServerName  string
}

type ProxyHandler struct {
	proxies []*httputil.ReverseProxy
	// [SECURITY] The index uses atomic operations because requests are served concurrently.
	index atomic.Uint64
}

func NewProxyHandler(targets []Target) (*ProxyHandler, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("at least one backend target is required")
	}

	proxies := make([]*httputil.ReverseProxy, 0, len(targets))
	for _, target := range targets {
		if target.URL == nil {
			return nil, fmt.Errorf("backend target must not be nil")
		}

		director := newDirector(target)
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			// [SECURITY] Connections are pinned to the validated startup address to reduce DNS rebinding risk.
			return (&net.Dialer{}).DialContext(ctx, network, target.DialAddress)
		}
		if target.URL.Scheme == "https" && target.ServerName != "" {
			transport.TLSClientConfig = &tls.Config{
				ServerName: target.ServerName,
				MinVersion: tls.VersionTLS12,
			}
		}

		proxies = append(proxies, &httputil.ReverseProxy{
			Rewrite: func(proxyRequest *httputil.ProxyRequest) {
				director(proxyRequest.Out)
			},
			Transport: transport,
		})
	}

	return &ProxyHandler{proxies: proxies}, nil
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	current := h.index.Add(1) - 1
	selected := h.proxies[current%uint64(len(h.proxies))]
	selected.ServeHTTP(w, r)
}
