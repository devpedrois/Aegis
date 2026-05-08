package test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	cfgpkg "github.com/user/aegis/internal/config"
	poolpkg "github.com/user/aegis/internal/pool"
	proxypkg "github.com/user/aegis/internal/proxy"
	ratelimitpkg "github.com/user/aegis/internal/ratelimit"
	securitypkg "github.com/user/aegis/internal/security"
)

type capturedRequest struct {
	mu      sync.Mutex
	host    string
	path    string
	headers http.Header
}

func TestSecurityRejectsRequestSmugglingCLAndTE(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodPost, "http://aegis.local/upload", strings.NewReader("0123456789"))
	req.RemoteAddr = "203.0.113.10:1234"
	req.ContentLength = 10
	req.Header.Set("Transfer-Encoding", "chunked")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"ambiguous request"}` {
		t.Fatalf("body = %q, want generic smuggling error", got)
	}
}

func TestSecurityRejectsRequestSmugglingWithZeroContentLength(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodPost, "http://aegis.local/upload", http.NoBody)
	req.RemoteAddr = "203.0.113.10:1234"
	req.ContentLength = 0
	req.Header.Set("Content-Length", "0")
	req.Header.Set("Transfer-Encoding", "chunked")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSecurityRejectsPathTraversal(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/../../../etc/passwd", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSecurityRejectsEncodedPathTraversal(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/%2e%2e/%2e%2e/etc/passwd", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSecurityRejectsDoubleEncodedPathTraversal(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.URL.Path = "/%252e%252e/%252e%252e/etc/passwd"
	req.URL.RawPath = "/%252e%252e/%252e%252e/etc/passwd"
	req.RequestURI = "/%252e%252e/%252e%252e/etc/passwd"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSecurityRejectsMalformedEscapedPath(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.URL.Path = "/%ZZ"
	req.URL.RawPath = "/%ZZ"
	req.RequestURI = "/%ZZ"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDirectorIgnoresClientHostForUpstreamRouting(t *testing.T) {
	t.Parallel()

	handler, captured := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), func(cfg *cfgpkg.Config) {
		cfg.Server.AllowedHosts = nil
	})

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Host = "evil.com"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if captured.host == "evil.com" {
		t.Fatalf("upstream host = %q, want backend host instead of client host", captured.host)
	}
}

func TestSecurityRejectsTraceMethod(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodTrace, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestSecurityRejectsConnectMethod(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodConnect, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestSecurityRejectsBodiesOverConfiguredLimit(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}), func(cfg *cfgpkg.Config) {
		cfg.Server.MaxBodyBytes = 4
	})

	req := httptest.NewRequest(http.MethodPost, "http://aegis.local/upload", bytes.NewBufferString("12345"))
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestSecurityRejectsChunkedBodyOverLimit(t *testing.T) {
	t.Parallel()

	var backendBytes atomic.Int64
	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err == nil {
			backendBytes.Store(int64(len(payload)))
		}
		w.WriteHeader(http.StatusOK)
	}), func(cfg *cfgpkg.Config) {
		cfg.Server.MaxBodyBytes = 4
	})

	req := httptest.NewRequest(http.MethodPost, "http://aegis.local/upload", bytes.NewBufferString("12345"))
	req.RemoteAddr = "203.0.113.10:1234"
	req.ContentLength = -1
	req.Header.Del("Content-Length")
	req.TransferEncoding = []string{"chunked"}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	if backendBytes.Load() > 4 {
		t.Fatalf("backend received %d bytes, want at most 4 before edge rejection", backendBytes.Load())
	}
}

func TestSecurityAllowsChunkedBodyWithinLimit(t *testing.T) {
	t.Parallel()

	var backendBytes atomic.Int64
	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		backendBytes.Store(int64(len(payload)))
		w.Header().Set("X-Body-Size", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
	}), func(cfg *cfgpkg.Config) {
		cfg.Server.MaxBodyBytes = 4
	})

	req := httptest.NewRequest(http.MethodPost, "http://aegis.local/upload", bytes.NewBufferString("1234"))
	req.RemoteAddr = "203.0.113.10:1234"
	req.ContentLength = -1
	req.Header.Del("Content-Length")
	req.TransferEncoding = []string{"chunked"}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if backendBytes.Load() != 4 {
		t.Fatalf("backend received %d bytes, want 4", backendBytes.Load())
	}
}

func TestSecurityRejectsChunkedBodyOverLimitEvenWhenBackendSkipsRead(t *testing.T) {
	t.Parallel()

	var backendCalls atomic.Int64
	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}), func(cfg *cfgpkg.Config) {
		cfg.Server.MaxBodyBytes = 4
	})

	req := httptest.NewRequest(http.MethodPost, "http://aegis.local/upload", bytes.NewBufferString("12345"))
	req.RemoteAddr = "203.0.113.10:1234"
	req.ContentLength = -1
	req.Header.Del("Content-Length")
	req.TransferEncoding = []string{"chunked"}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}

	if backendCalls.Load() > 1 {
		t.Fatalf("backendCalls = %d, want at most one attempt", backendCalls.Load())
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}

	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("X-Frame-Options = %q, want %q", got, "DENY")
	}
}

func TestSecurityReplacesServerHeaderAndStripsPoweredBy(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "nginx")
		w.Header().Set("X-Powered-By", "php")
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Server"); got != "aegis" {
		t.Fatalf("Server = %q, want %q", got, "aegis")
	}

	if got := rec.Header().Get("X-Powered-By"); got != "" {
		t.Fatalf("X-Powered-By = %q, want empty", got)
	}
}

func TestDirectorStripsHopByHopHeaders(t *testing.T) {
	t.Parallel()

	handler, captured := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Connection", "X-Custom")
	req.Header.Set("X-Custom", "malicious")
	req.Header.Set("Transfer-Encoding", "chunked")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if captured.headers != nil {
		if got := captured.headers.Get("X-Custom"); got != "" {
			t.Fatalf("upstream X-Custom = %q, want stripped", got)
		}
	}
}

func TestDirectorStripsAnnouncedRequestTrailers(t *testing.T) {
	t.Parallel()

	var receivedTrailer http.Header
	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		receivedTrailer = r.Trailer.Clone()
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodPost, "http://aegis.local/check", bytes.NewBufferString("ok"))
	req.RemoteAddr = "203.0.113.10:1234"
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	req.Header.Set("Trailer", "X-Injected-Trailer")
	req.Trailer = http.Header{"X-Injected-Trailer": []string{"malicious"}}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if got := receivedTrailer.Get("X-Injected-Trailer"); got != "" {
		t.Fatalf("backend trailer = %q, want stripped", got)
	}
}

func TestDirectorReplacesSpoofedForwardedFor(t *testing.T) {
	t.Parallel()

	handler, captured := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:4567"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	if got := captured.headers.Get("X-Forwarded-For"); got != "203.0.113.10" {
		t.Fatalf("upstream X-Forwarded-For = %q, want %q", got, "203.0.113.10")
	}
}

func TestRequestIDsAreUniqueAcrossSequentialRequests(t *testing.T) {
	t.Parallel()

	var requestIDs []string
	var mu sync.Mutex
	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestIDs = append(requestIDs, r.Header.Get("X-Request-ID"))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}), nil)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
		req.RemoteAddr = "203.0.113.10:1234"

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want %d", i, rec.Code, http.StatusOK)
		}
	}

	if len(requestIDs) != 2 {
		t.Fatalf("requestIDs count = %d, want 2", len(requestIDs))
	}

	if requestIDs[0] == "" || requestIDs[1] == "" {
		t.Fatalf("requestIDs = %#v, want non-empty values", requestIDs)
	}

	if requestIDs[0] == requestIDs[1] {
		t.Fatalf("requestIDs = %#v, want unique values per request", requestIDs)
	}
}

func TestDirectorReplacesClientSuppliedRequestID(t *testing.T) {
	t.Parallel()

	handler, captured := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:4567"
	req.Header.Set("X-Request-ID", "attacker-controlled")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	got := captured.headers.Get("X-Request-ID")
	if got == "" {
		t.Fatal("upstream X-Request-ID is empty, want generated identifier")
	}

	if got == "attacker-controlled" {
		t.Fatalf("upstream X-Request-ID = %q, want edge-generated value", got)
	}
}

func TestHopByHopHeadersDoNotPersistAcrossSequentialRequests(t *testing.T) {
	t.Parallel()

	var receivedHeaders []http.Header
	var mu sync.Mutex
	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedHeaders = append(receivedHeaders, r.Header.Clone())
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}), nil)

	first := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	first.RemoteAddr = "203.0.113.10:1234"
	first.Header.Set("Connection", "X-Leak")
	first.Header.Set("X-Leak", "malicious")
	firstRec := httptest.NewRecorder()
	handler.ServeHTTP(firstRec, first)

	second := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	second.RemoteAddr = "203.0.113.10:1234"
	secondRec := httptest.NewRecorder()
	handler.ServeHTTP(secondRec, second)

	if len(receivedHeaders) != 2 {
		t.Fatalf("receivedHeaders count = %d, want 2", len(receivedHeaders))
	}

	if got := receivedHeaders[0].Get("X-Leak"); got != "" {
		t.Fatalf("first request upstream X-Leak = %q, want stripped", got)
	}

	if got := receivedHeaders[1].Get("X-Leak"); got != "" {
		t.Fatalf("second request upstream X-Leak = %q, want no cross-request leak", got)
	}
}

func TestInternalBackendHeaderIsNotLeakedToClient(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Aegis-Backend"); got != "" {
		t.Fatalf("X-Aegis-Backend = %q, want hidden internal metadata", got)
	}
}

func TestSensitiveResponseTrailersAreNotLeakedToClient(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Trailer", "X-Powered-By")
		w.Header().Add("Trailer", "X-Aegis-Backend")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		w.Header().Set("X-Powered-By", "backend-trailer")
		w.Header().Set("X-Aegis-Backend", "backend-secret")
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	result := rec.Result()

	if got := result.Trailer.Get("X-Powered-By"); got != "" {
		t.Fatalf("response trailer X-Powered-By = %q, want stripped", got)
	}

	if got := result.Trailer.Get("X-Aegis-Backend"); got != "" {
		t.Fatalf("response trailer X-Aegis-Backend = %q, want stripped", got)
	}
}

func TestAllowedHostValidationAcceptsPortAndCaseInsensitiveHost(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), func(cfg *cfgpkg.Config) {
		cfg.Server.AllowedHosts = []string{"Aegis.Local"}
	})

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"
	req.Host = "AEGIS.LOCAL:8080"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestValidateRejectsLoopbackBackendsWithoutDevelopmentBypass(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Backends = []cfgpkg.BackendConfig{
		{URL: "http://127.0.0.1:8081", Weight: 1},
	}

	if err := cfgpkg.Validate(cfg); err == nil {
		t.Fatal("Validate() error = nil, want SSRF rejection")
	}
}

func TestValidateRejectsMetadataBackends(t *testing.T) {
	t.Parallel()

	cfg := cfgpkg.NewDefaultConfig()
	cfg.Backends = []cfgpkg.BackendConfig{
		{URL: "http://169.254.169.254/latest/meta-data", Weight: 1},
	}

	if err := cfgpkg.Validate(cfg); err == nil {
		t.Fatal("Validate() error = nil, want metadata SSRF rejection")
	}
}

func TestProxyHidesUpstream500Bodies(t *testing.T) {
	t.Parallel()

	handler, _ := newSecurityProxyHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "stack trace: upstream secret", http.StatusInternalServerError)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}

	body := strings.TrimSpace(rec.Body.String())
	if body != `{"error":"bad gateway"}` {
		t.Fatalf("body = %q, want generic 502 body", body)
	}

	if strings.Contains(body, "secret") || strings.Contains(body, "stack trace") {
		t.Fatalf("body = %q, want no upstream details", body)
	}
}

func TestRecoveryReturnsGenericBadGatewayOnPanic(t *testing.T) {
	t.Parallel()

	handler := securitypkg.SecurityHeaders(
		proxypkg.RequestLogger(
			proxypkg.Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				panic("boom")
			})),
		),
	)

	req := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	req.RemoteAddr = "203.0.113.10:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}

	body := strings.TrimSpace(rec.Body.String())
	if body != `{"error":"bad gateway"}` {
		t.Fatalf("body = %q, want generic panic body", body)
	}

	if strings.Contains(body, "panic") || strings.Contains(body, "stack") {
		t.Fatalf("body = %q, want no panic details", body)
	}
}

func TestRateLimiterUsesRemoteAddrEvenWhenForwardedForChanges(t *testing.T) {
	t.Parallel()

	limiter := ratelimitpkg.NewRateLimiter(1, 1)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	firstReq := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	firstReq.RemoteAddr = "203.0.113.10:1234"
	firstReq.Header.Set("X-Forwarded-For", "1.1.1.1")
	handler.ServeHTTP(first, firstReq)

	second := httptest.NewRecorder()
	secondReq := httptest.NewRequest(http.MethodGet, "http://aegis.local/check", nil)
	secondReq.RemoteAddr = "203.0.113.10:1234"
	secondReq.Header.Set("X-Forwarded-For", "8.8.8.8")
	handler.ServeHTTP(second, secondReq)

	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d for same RemoteAddr bucket", second.Code, http.StatusTooManyRequests)
	}
}

func newSecurityProxyHandler(t *testing.T, backendHandler http.Handler, configure func(*cfgpkg.Config)) (http.Handler, *capturedRequest) {
	t.Helper()

	captured := &capturedRequest{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.mu.Lock()
		captured.host = r.Host
		captured.path = r.URL.Path
		captured.headers = r.Header.Clone()
		captured.mu.Unlock()
		backendHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(backend.Close)

	parsedBackendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	poolConfigs := []cfgpkg.BackendConfig{
		{
			URL:           backend.URL,
			Weight:        1,
			PinnedAddress: parsedBackendURL.Host,
			ServerName:    parsedBackendURL.Hostname(),
			OriginalHost:  parsedBackendURL.Host,
		},
	}
	pool, err := poolpkg.NewPool(poolConfigs, proxypkg.NewDirector)
	if err != nil {
		t.Fatalf("NewPool() error = %v", err)
	}
	proxyHandler := proxypkg.NewProxyHandler(pool)
	cfg := cfgpkg.NewDefaultConfig()
	cfg.Server.AllowedHosts = []string{"aegis.local"}
	if configure != nil {
		configure(cfg)
	}

	handler := securitypkg.SecurityHeaders(
		proxypkg.RequestLogger(
			proxypkg.Recovery(
				securitypkg.ValidateRequest(securitypkg.RequestValidationConfig{
					MaxBodyBytes: cfg.Server.MaxBodyBytes,
					AllowedHosts: cfg.Server.AllowedHosts,
				})(
					ratelimitpkg.NewRateLimiter(1000, 1000).Middleware(proxyHandler),
				),
			),
		),
	)

	captured.host = parsedBackendURL.Host
	return handler, captured
}
