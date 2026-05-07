package test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	proxypkg "github.com/user/aegis/internal/proxy"
)

func TestProxyHandlerRoundRobin(t *testing.T) {
	t.Parallel()

	backendA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"backend": "a"})
	}))
	defer backendA.Close()

	backendB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"backend": "b"})
	}))
	defer backendB.Close()

	backendAURL, err := url.Parse(backendA.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	backendBURL, err := url.Parse(backendB.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	handler, err := proxypkg.NewProxyHandler([]proxypkg.Target{
		{
			URL:         backendAURL,
			HostHeader:  backendAURL.Host,
			DialAddress: backendAURL.Host,
			ServerName:  backendAURL.Hostname(),
		},
		{
			URL:         backendBURL,
			HostHeader:  backendBURL.Host,
			DialAddress: backendBURL.Host,
			ServerName:  backendBURL.Hostname(),
		},
	})
	if err != nil {
		t.Fatalf("NewProxyHandler() error = %v", err)
	}

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

func TestProxyHandlerSetsXForwardedForFromRemoteAddr(t *testing.T) {
	t.Parallel()

	var receivedXForwardedFor string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedXForwardedFor = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	handler, err := proxypkg.NewProxyHandler([]proxypkg.Target{
		{
			URL:         backendURL,
			HostHeader:  backendURL.Host,
			DialAddress: backendURL.Host,
			ServerName:  backendURL.Hostname(),
		},
	})
	if err != nil {
		t.Fatalf("NewProxyHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:4567"
	req.Header.Set("X-Forwarded-For", "198.51.100.99")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("ServeHTTP() status = %d, want %d", recorder.Code, http.StatusOK)
	}

	if receivedXForwardedFor != "203.0.113.10" {
		t.Fatalf("X-Forwarded-For = %q, want %q", receivedXForwardedFor, "203.0.113.10")
	}
}
