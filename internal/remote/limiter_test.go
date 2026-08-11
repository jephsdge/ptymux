package remote

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyedRateLimiterRefills(t *testing.T) {
	now := time.Unix(100, 0)
	limiter := newKeyedRateLimiter(2, 2)
	limiter.now = func() time.Time { return now }
	if !limiter.Allow("client") || !limiter.Allow("client") {
		t.Fatal("initial burst was not available")
	}
	if limiter.Allow("client") || limiter.Available("client") {
		t.Fatal("exhausted bucket still allowed a token")
	}
	now = now.Add(500 * time.Millisecond)
	if !limiter.Available("client") || !limiter.Allow("client") {
		t.Fatal("bucket did not refill one token")
	}
	if limiter.Allow("client") {
		t.Fatal("refilled token was not consumed")
	}
}

func TestKeyedRateLimiterConcurrentBurstIsAtomic(t *testing.T) {
	limiter := newKeyedRateLimiter(1, 1)
	start := make(chan struct{})
	var allowed int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if limiter.Allow("client") {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if allowed != 1 {
		t.Fatalf("allowed attempts = %d, want 1", allowed)
	}
}

func TestKeyedRateLimiterRefundsSuccessfulAttempt(t *testing.T) {
	limiter := newKeyedRateLimiter(1, 1)
	if !limiter.Allow("client") {
		t.Fatal("initial token was not available")
	}
	if limiter.Allow("client") {
		t.Fatal("exhausted bucket allowed another token")
	}
	limiter.Refund("client")
	if !limiter.Allow("client") {
		t.Fatal("refunded token was not available")
	}
}

func TestRemoteAddressKey(t *testing.T) {
	request := &http.Request{RemoteAddr: "127.0.0.1:8443"}
	if got := remoteAddressKey(request); got != "127.0.0.1" {
		t.Fatalf("remoteAddressKey = %q", got)
	}
	request.RemoteAddr = "local"
	if got := remoteAddressKey(request); got != "local" {
		t.Fatalf("remoteAddressKey fallback = %q", got)
	}
}
