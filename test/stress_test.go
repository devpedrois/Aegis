//go:build stress

package test

import (
	"io"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStressConcurrentLoad(t *testing.T) {
	backends := []testBackendConfig{
		{Name: "backend-a", Weight: 1, Control: newTestBackendControl()},
		{Name: "backend-b", Weight: 1, Control: newTestBackendControl()},
		{Name: "backend-c", Weight: 1, Control: newTestBackendControl()},
	}

	baselineGoroutines := runtime.NumGoroutine()
	server, _, _, cleanup := newTestProxy(t, backends)

	client := &http.Client{Timeout: 2 * time.Second}
	var okCount atomic.Int64
	var rateLimitedCount atomic.Int64
	var unexpectedStatus atomic.Int64
	var requestErrors atomic.Int64

	var wg sync.WaitGroup
	for workerIndex := 0; workerIndex < 100; workerIndex++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for requestIndex := 0; requestIndex < 10; requestIndex++ {
				response, err := client.Get("http://" + server.Addr + "/stress")
				if err != nil {
					requestErrors.Add(1)
					continue
				}

				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()

				switch response.StatusCode {
				case http.StatusOK:
					okCount.Add(1)
				case http.StatusTooManyRequests:
					rateLimitedCount.Add(1)
				default:
					unexpectedStatus.Add(1)
				}
			}
		}()
	}

	wg.Wait()
	cleanup()

	if requestErrors.Load() != 0 {
		t.Fatalf("requestErrors = %d, want 0", requestErrors.Load())
	}

	if unexpectedStatus.Load() != 0 {
		t.Fatalf("unexpectedStatus = %d, want only 200 or 429 responses", unexpectedStatus.Load())
	}

	if rateLimitedCount.Load() == 0 {
		t.Fatal("rateLimitedCount = 0, want limiter to block excess load")
	}

	if got := okCount.Load() + rateLimitedCount.Load(); got != 1000 {
		t.Fatalf("handled requests = %d, want 1000", got)
	}

	runtime.GC()
	requireEventually(t, 2*time.Second, func() bool {
		return runtime.NumGoroutine() <= baselineGoroutines+5
	})
}
