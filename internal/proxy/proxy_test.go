package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/user/aegis/internal/config"
	"github.com/user/aegis/internal/pool"
)

func TestProxyHandlerUsesPinnedDialAddress(t *testing.T) {
	t.Parallel()

	var receivedHost string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendPool, err := pool.NewPool([]config.BackendConfig{
		{
			URL:           "http://backend.example.com",
			Weight:        1,
			PinnedAddress: backend.Listener.Addr().String(),
			ServerName:    "backend.example.com",
			OriginalHost:  "backend.example.com",
		},
	}, NewDirector)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}

	handler := NewProxyHandler(backendPool)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:4567"
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("ServeHTTP() status = %d, want %d", recorder.Code, http.StatusOK)
	}

	if receivedHost != "backend.example.com" {
		t.Fatalf("received host = %q, want %q", receivedHost, "backend.example.com")
	}
}
