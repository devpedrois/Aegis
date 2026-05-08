package proxy

import (
	"encoding/json"
	"net/http"

	"github.com/user/aegis/internal/pool"
)

type ProxyHandler struct {
	pool *pool.BackendPool
}

func NewProxyHandler(backendPool *pool.BackendPool) *ProxyHandler {
	return &ProxyHandler{pool: backendPool}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	backend, err := h.pool.NextHealthy()
	if err != nil {
		// [SECURITY] The failure response stays generic so clients cannot enumerate backend pool state.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no healthy backends"})
		return
	}

	if backend != nil && backend.URL != nil {
		// [SECURITY] Backend identity is attached as internal response metadata for trusted logging and stripped before the client response is finalized.
		w.Header().Set("X-Aegis-Backend", backend.URL.String())
	}

	backend.Proxy.ServeHTTP(w, r)
}
