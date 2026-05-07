package test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ratelimitpkg "github.com/user/aegis/internal/ratelimit"
	securitypkg "github.com/user/aegis/internal/security"
)

func TestTokenBucketConsumesAndRejectsWhenEmpty(t *testing.T) {
	t.Parallel()

	bucket := ratelimitpkg.NewBucket(1, 2)

	if !bucket.TryConsume(1) {
		t.Fatal("first TryConsume(1) = false, want true")
	}

	if !bucket.TryConsume(1) {
		t.Fatal("second TryConsume(1) = false, want true")
	}

	if bucket.TryConsume(1) {
		t.Fatal("third TryConsume(1) = true, want false")
	}
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	t.Parallel()

	bucket := ratelimitpkg.NewBucket(20, 1)
	if !bucket.TryConsume(1) {
		t.Fatal("TryConsume(1) = false, want true")
	}

	time.Sleep(60 * time.Millisecond)

	if !bucket.TryConsume(1) {
		t.Fatal("TryConsume(1) after refill = false, want true")
	}
}

func TestTokenBucketSetRateChangesRefillBehavior(t *testing.T) {
	t.Parallel()

	bucket := ratelimitpkg.NewBucket(0.1, 1)
	if !bucket.TryConsume(1) {
		t.Fatal("TryConsume(1) = false, want true")
	}

	time.Sleep(20 * time.Millisecond)
	if bucket.TryConsume(1) {
		t.Fatal("TryConsume(1) before SetRate = true, want false")
	}

	bucket.SetRate(100)
	time.Sleep(20 * time.Millisecond)

	if !bucket.TryConsume(1) {
		t.Fatal("TryConsume(1) after SetRate = false, want true")
	}
}

func TestTokenBucketTryConsumeIsThreadSafe(t *testing.T) {
	t.Parallel()

	bucket := ratelimitpkg.NewBucket(0, 50)

	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if bucket.TryConsume(1) {
				allowed.Add(1)
			}
		}()
	}

	wg.Wait()

	if got := allowed.Load(); got != 50 {
		t.Fatalf("allowed = %d, want 50", got)
	}
}

func TestRateLimiterAllowsRequestsWithinLimit(t *testing.T) {
	t.Parallel()

	limiter := ratelimitpkg.NewRateLimiter(2, 2)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	recorderA := httptest.NewRecorder()
	requestA := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	requestA.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(recorderA, requestA)

	recorderB := httptest.NewRecorder()
	requestB := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	requestB.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(recorderB, requestB)

	if recorderA.Code != http.StatusOK || recorderB.Code != http.StatusOK {
		t.Fatalf("status codes = [%d,%d], want [200,200]", recorderA.Code, recorderB.Code)
	}
}

func TestRateLimiterRejectsExcessRequestsWithRetryAfterJSON(t *testing.T) {
	t.Parallel()

	limiter := ratelimitpkg.NewRateLimiter(1, 1)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	firstRequest := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	firstRequest.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(first, firstRequest)

	second := httptest.NewRecorder()
	secondRequest := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	secondRequest.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(second, secondRequest)

	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want %d", second.Code, http.StatusTooManyRequests)
	}

	if got := second.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want %q", got, "1")
	}

	var payload map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if payload["error"] != "rate limit exceeded" {
		t.Fatalf("payload error = %#v, want %q", payload["error"], "rate limit exceeded")
	}

	if payload["retry_after"] != float64(1) {
		t.Fatalf("payload retry_after = %#v, want %v", payload["retry_after"], 1)
	}
}

func TestRateLimiterUsesSeparateBucketsPerIP(t *testing.T) {
	t.Parallel()

	limiter := ratelimitpkg.NewRateLimiter(1, 1)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	firstIP := httptest.NewRecorder()
	firstIPRequest := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	firstIPRequest.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(firstIP, firstIPRequest)

	secondIP := httptest.NewRecorder()
	secondIPRequest := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	secondIPRequest.RemoteAddr = "203.0.113.11:1234"
	handler.ServeHTTP(secondIP, secondIPRequest)

	if firstIP.Code != http.StatusOK || secondIP.Code != http.StatusOK {
		t.Fatalf("status codes = [%d,%d], want [200,200]", firstIP.Code, secondIP.Code)
	}
}

func TestExtractIPUsesRemoteAddrOnly(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	request.RemoteAddr = "203.0.113.10:4567"
	request.Header.Set("X-Forwarded-For", "198.51.100.5")
	request.Header.Set("X-Real-IP", "198.51.100.6")

	if got := securitypkg.ExtractIP(request); got != "203.0.113.10" {
		t.Fatalf("ExtractIP() = %q, want %q", got, "203.0.113.10")
	}
}

func TestRateLimiterSetGlobalRatePropagatesToExistingBuckets(t *testing.T) {
	t.Parallel()

	limiter := ratelimitpkg.NewRateLimiter(0.1, 1)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	request.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(first, request)

	time.Sleep(20 * time.Millisecond)

	blocked := httptest.NewRecorder()
	blockedRequest := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	blockedRequest.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(blocked, blockedRequest)

	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked status = %d, want %d", blocked.Code, http.StatusTooManyRequests)
	}

	limiter.SetGlobalRate(100)
	time.Sleep(20 * time.Millisecond)

	allowed := httptest.NewRecorder()
	allowedRequest := httptest.NewRequest(http.MethodGet, "http://aegis.local/ok", nil)
	allowedRequest.RemoteAddr = "203.0.113.10:1234"
	handler.ServeHTTP(allowed, allowedRequest)

	if allowed.Code != http.StatusOK {
		t.Fatalf("allowed status after SetGlobalRate = %d, want %d", allowed.Code, http.StatusOK)
	}
}
