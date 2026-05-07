package ratelimit

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

const bucketIdleTTL = 5 * time.Minute

var cleanupDeleteHook = func(*TokenBucket) {}

func StartCleanup(ctx context.Context, rl *RateLimiter, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				// [SECURITY] Cleanup workers must stop on shutdown to avoid lingering goroutines under repeated restart conditions.
				return
			case <-ticker.C:
				cleanupStaleBuckets(rl, time.Now())
			}
		}
	}()
}

func cleanupStaleBuckets(rl *RateLimiter, now time.Time) {
	rl.buckets.Range(func(key, value any) bool {
		ip, ipOK := key.(string)
		bucket, bucketOK := value.(*TokenBucket)
		if !ipOK || !bucketOK {
			return true
		}

		bucket.mu.Lock()
		lastAccess := bucket.lastAccess
		bucket.mu.Unlock()

		if now.Sub(lastAccess) > bucketIdleTTL {
			cleanupDeleteHook(bucket)

			bucket.mu.Lock()
			lastAccess = bucket.lastAccess
			bucket.mu.Unlock()
			if now.Sub(lastAccess) <= bucketIdleTTL {
				return true
			}

			if !rl.buckets.CompareAndDelete(ip, bucket) {
				return true
			}

			// [SECURITY] Idle bucket eviction limits attacker-driven map growth from rotating source addresses.
			// [SECURITY] Cleanup logs mask client IPs to avoid exposing full addresses in operational output.
			log.Printf("DEBUG Cleaned up bucket for IP %s", maskIP(ip))
		}

		return true
	})
}

func maskIP(raw string) string {
	ip := net.ParseIP(raw)
	if ip == nil {
		return "unknown"
	}

	if ipv4 := ip.To4(); ipv4 != nil {
		return fmt.Sprintf("%d.%d.x.x", ipv4[0], ipv4[1])
	}

	parts := strings.Split(ip.String(), ":")
	if len(parts) >= 2 {
		return parts[0] + ":" + parts[1] + ":x:x:x:x:x:x"
	}

	return "masked"
}
