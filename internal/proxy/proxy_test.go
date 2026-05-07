package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestProxyHandlerUsesPinnedDialAddress(t *testing.T) {
	t.Parallel()

	var receivedHost string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	backendURL, err := url.Parse("http://backend.example.com")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	pinnedURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	handler, err := NewProxyHandler([]Target{
		{
			URL:         backendURL,
			HostHeader:  "backend.example.com",
			DialAddress: pinnedURL.Host,
			ServerName:  "backend.example.com",
		},
	})
	if err != nil {
		t.Fatalf("NewProxyHandler() error = %v", err)
	}

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
