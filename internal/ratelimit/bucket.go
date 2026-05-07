package ratelimit

import (
	"sync"
	"time"
)

type TokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
	lastAccess time.Time
	// [SECURITY] Each bucket uses its own mutex so consume and refill stay atomic per client IP under concurrent abuse.
	mu sync.Mutex
}

func NewBucket(rate, burst float64) *TokenBucket {
	if rate < 0 {
		rate = 0
	}

	if burst < 0 {
		burst = 0
	}

	now := time.Now()
	return &TokenBucket{
		tokens:     burst,
		maxTokens:  burst,
		refillRate: rate,
		lastRefill: now,
		lastAccess: now,
	}
}

func (b *TokenBucket) TryConsume(n float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.refillLocked(now)
	b.lastAccess = now

	if b.tokens >= n {
		b.tokens -= n
		return true
	}

	return false
}

func (b *TokenBucket) SetRate(rate float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if rate < 0 {
		rate = 0
	}

	// [SECURITY] Rate changes are accepted only from trusted runtime control paths, never from client-controlled input.
	b.refillRate = rate
}

func (b *TokenBucket) refillLocked(now time.Time) {
	elapsed := now.Sub(b.lastRefill)
	if elapsed > 0 {
		b.tokens += elapsed.Seconds() * b.refillRate
		if b.tokens > b.maxTokens {
			b.tokens = b.maxTokens
		}
	}

	b.lastRefill = now
}

func (b *TokenBucket) availableTokens() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked(time.Now())
	return b.tokens
}
