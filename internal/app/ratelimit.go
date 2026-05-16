package app

import (
	"sync"
	"time"
)

// rateLimiter implements a simple per-sender token-bucket that prevents any
// single user from flooding the bot (and draining LLM API quota).
//
// Design: each sender gets a bucket with `capacity` tokens refilled at `rate`
// per `window`. One incoming message costs 1 token. If the bucket is empty the
// message is silently throttled and a friendly message is returned.
type rateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*tokenBucket
	capacity int           // max burst
	window   time.Duration // refill period
}

type tokenBucket struct {
	tokens    int
	lastReset time.Time
}

const (
	defaultRateCapacity = 10              // max 10 messages per window
	defaultRateWindow   = 1 * time.Minute // per 1 minute
)

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		buckets:  make(map[string]*tokenBucket),
		capacity: defaultRateCapacity,
		window:   defaultRateWindow,
	}
}

// allow returns true if the sender is within their quota, false if throttled.
func (r *rateLimiter) allow(sender string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	bucket, ok := r.buckets[sender]
	if !ok {
		r.buckets[sender] = &tokenBucket{tokens: r.capacity - 1, lastReset: now}
		return true
	}

	// Refill on window boundary.
	if now.Sub(bucket.lastReset) >= r.window {
		bucket.tokens = r.capacity
		bucket.lastReset = now
	}

	if bucket.tokens <= 0 {
		return false
	}
	bucket.tokens--
	return true
}

// pruneExpiredBuckets removes stale buckets older than 5 windows.
// Cheap maintenance; can be called periodically or on each request.
func (r *rateLimiter) pruneExpiredBuckets() {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-5 * r.window)
	for k, b := range r.buckets {
		if b.lastReset.Before(cutoff) {
			delete(r.buckets, k)
		}
	}
}
