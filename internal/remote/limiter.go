package remote

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const maxRateLimiterEntries = 4096

type rateBucket struct {
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

type keyedRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	buckets map[string]rateBucket
	now     func() time.Time
}

func newKeyedRateLimiter(rate float64, burst int) *keyedRateLimiter {
	return &keyedRateLimiter{
		rate:    rate,
		burst:   float64(burst),
		buckets: make(map[string]rateBucket),
		now:     time.Now,
	}
}

func (l *keyedRateLimiter) Available(key string) bool {
	return l.check(key, false)
}

func (l *keyedRateLimiter) Allow(key string) bool {
	return l.check(key, true)
}

func (l *keyedRateLimiter) Refund(key string) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[key]
	if !ok {
		return
	}
	bucket = l.refill(bucket, now)
	bucket.tokens++
	if bucket.tokens > l.burst {
		bucket.tokens = l.burst
	}
	l.buckets[key] = bucket
}

func (l *keyedRateLimiter) check(key string, consume bool) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	bucket, ok := l.buckets[key]
	if !ok {
		if !consume {
			return true
		}
		if len(l.buckets) >= maxRateLimiterEntries {
			l.prune(now)
			if len(l.buckets) >= maxRateLimiterEntries {
				return false
			}
		}
		bucket = rateBucket{tokens: l.burst, updated: now}
	}
	bucket = l.refill(bucket, now)
	if bucket.tokens < 1 {
		l.buckets[key] = bucket
		return false
	}
	if consume {
		bucket.tokens--
	}
	l.buckets[key] = bucket
	return true
}

func (l *keyedRateLimiter) refill(bucket rateBucket, now time.Time) rateBucket {
	elapsed := now.Sub(bucket.updated).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * l.rate
		if bucket.tokens > l.burst {
			bucket.tokens = l.burst
		}
	}
	bucket.updated = now
	bucket.lastSeen = now
	return bucket
}

func (l *keyedRateLimiter) prune(now time.Time) {
	idleFor := time.Duration((l.burst / l.rate) * 2 * float64(time.Second))
	if idleFor < time.Minute {
		idleFor = time.Minute
	}
	for key, bucket := range l.buckets {
		if now.Sub(bucket.lastSeen) >= idleFor {
			delete(l.buckets, key)
		}
	}
}

func remoteAddressKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}
