package test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	proxypkg "github.com/user/aegis/internal/proxy"
)

func TestProxyHandlerUsesHealthyPoolBackends(t *testing.T) {
	t.Parallel()

	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"backend": "a"})
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"backend": "b"})
	}))
	defer backendB.Close()

	pool := newHTTPPool(t, []string{backendA.URL, backendB.URL})
	handler := proxypkg.NewProxyHandler(pool)

	server := httptest.NewServer(handler)
	defer server.Close()

	want := []string{"a", "b", "a", "b"}
	for i, expectedBackend := range want {
		resp, err := http.Get(server.URL + "/test")
		if err != nil {
			t.Fatalf("request %d error = %v", i, err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}

		var payload map[string]string
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}

		if payload["backend"] != expectedBackend {
			t.Fatalf("request %d backend = %q, want %q", i, payload["backend"], expectedBackend)
		}
	}
}

func TestProxyHandlerReturns503WhenNoHealthyBackendExists(t *testing.T) {
	t.Parallel()

	pool := newTestPool(t, []backendSpec{{name: "a", weight: 1}})
	pool.MarkUnhealthy("http://a.example")

	handler := proxypkg.NewProxyHandler(pool)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:4567"

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("ServeHTTP() status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}

	if got := strings.TrimSpace(recorder.Body.String()); got != `{"error":"no healthy backends"}` {
		t.Fatalf("ServeHTTP() body = %q, want generic 503 body", got)
	}
}
