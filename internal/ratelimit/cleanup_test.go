package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestStartCleanupRemovesInactiveBuckets(t *testing.T) {
	limiter := NewRateLimiter(1, 1)
	bucket := limiter.getOrCreateBucket("203.0.113.10")

	bucket.mu.Lock()
	bucket.lastAccess = time.Now().Add(-6 * time.Minute)
	bucket.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartCleanup(ctx, limiter, 10*time.Millisecond)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := limiter.buckets.Load("203.0.113.10"); !ok {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("inactive bucket still exists after cleanup interval")
}

func TestCleanupSkipsBucketTouchedDuringSweep(t *testing.T) {
	limiter := NewRateLimiter(1, 1)
	bucket := limiter.getOrCreateBucket("203.0.113.10")

	bucket.mu.Lock()
	bucket.lastAccess = time.Now().Add(-6 * time.Minute)
	bucket.mu.Unlock()

	originalHook := cleanupDeleteHook
	cleanupDeleteHook = func(bucket *TokenBucket) {
		bucket.mu.Lock()
		bucket.lastAccess = time.Now()
		bucket.mu.Unlock()
	}
	defer func() {
		cleanupDeleteHook = originalHook
	}()

	cleanupStaleBuckets(limiter, time.Now())

	if _, ok := limiter.buckets.Load("203.0.113.10"); !ok {
		t.Fatal("bucket was deleted after being touched during cleanup")
	}
}
