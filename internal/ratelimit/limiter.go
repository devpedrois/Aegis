package ratelimit

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"sync"

	"github.com/user/aegis/internal/security"
)

type RateLimiter struct {
	buckets sync.Map
	rate    float64
	burst   float64
	// [SECURITY] Global limiter settings use RWMutex so request paths can read stable values while trusted control paths update rates.
	mu sync.RWMutex
}

func NewRateLimiter(rate, burst float64) *RateLimiter {
	if rate < 0 {
		rate = 0
	}

	if burst < 0 {
		burst = 0
	}

	return &RateLimiter{
		rate:  rate,
		burst: burst,
	}
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// [SECURITY] Source identity for rate limiting is derived from RemoteAddr only because client-sent forwarding headers are spoofable.
		ip := security.ExtractIP(r)
		bucket := rl.getOrCreateBucket(ip)
		if !bucket.TryConsume(1) {
			retryAfter := int(math.Ceil(1.0 / rl.currentRate()))
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			// [SECURITY] The rejection body is intentionally generic so attackers do not learn limiter internals.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":       "rate limit exceeded",
				"retry_after": retryAfter,
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) SetGlobalRate(rate float64) {
	if rate <= 0 {
		return
	}

	rl.mu.Lock()
	rl.rate = rate
	rl.mu.Unlock()

	rl.buckets.Range(func(_, value any) bool {
		bucket, ok := value.(*TokenBucket)
		if ok {
			bucket.SetRate(rate)
		}

		return true
	})
}

func (rl *RateLimiter) BlockedCount() int {
	count := 0
	rl.buckets.Range(func(_, value any) bool {
		bucket, ok := value.(*TokenBucket)
		if ok && bucket.availableTokens() <= 0 {
			count++
		}

		return true
	})

	return count
}

func (rl *RateLimiter) getOrCreateBucket(ip string) *TokenBucket {
	if bucket, ok := rl.buckets.Load(ip); ok {
		return bucket.(*TokenBucket)
	}

	rl.mu.RLock()
	rate := rl.rate
	burst := rl.burst
	rl.mu.RUnlock()

	candidate := NewBucket(rate, burst)
	actual, _ := rl.buckets.LoadOrStore(ip, candidate)
	return actual.(*TokenBucket)
}

func (rl *RateLimiter) currentRate() float64 {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	if rl.rate <= 0 {
		return 1
	}

	return rl.rate
}
