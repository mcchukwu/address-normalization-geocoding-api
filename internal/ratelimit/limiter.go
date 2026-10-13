// Package ratelimit is a per-tenant token bucket held in process memory. One node means no
// shared store is needed; scaling out would move the buckets to Redis or similar.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Decision is the outcome of one Allow call.
type Decision struct {
	Allowed           bool
	Limit             int // requests per minute
	Remaining         int
	ResetSeconds      int // until the bucket is full again
	RetryAfterSeconds int // until the request would be allowed (0 when allowed)
}

type bucket struct {
	tokens float64
	last   time.Time
	limit  int
}

// Limiter holds one bucket per tenant.
type Limiter struct {
	Now func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

// New returns an empty Limiter.
func New() *Limiter { return &Limiter{buckets: map[string]*bucket{}} }

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Allow takes cost tokens from the tenant's bucket, whose size and refill rate are perMin
// requests a minute. A cost larger than the bucket is capped to the bucket so a single big
// call is not impossible, only expensive.
func (l *Limiter) Allow(tenant string, perMin, cost int) Decision {
	if perMin < 1 {
		perMin = 1
	}
	if cost < 1 {
		cost = 1
	}
	if cost > perMin {
		cost = perMin
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[tenant]
	if !ok {
		b = &bucket{tokens: float64(perMin), last: now, limit: perMin}
		l.buckets[tenant] = b
	}
	if b.limit != perMin { // limit changed: keep the same fill ratio
		b.tokens = b.tokens / float64(b.limit) * float64(perMin)
		b.limit = perMin
	}
	rate := float64(perMin) / 60.0
	b.tokens = math.Min(float64(perMin), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now

	d := Decision{Limit: perMin}
	if b.tokens >= float64(cost) {
		b.tokens -= float64(cost)
		d.Allowed = true
	} else {
		d.RetryAfterSeconds = int(math.Ceil((float64(cost) - b.tokens) / rate))
		if d.RetryAfterSeconds < 1 {
			d.RetryAfterSeconds = 1
		}
	}
	d.Remaining = int(math.Floor(b.tokens))
	d.ResetSeconds = int(math.Ceil((float64(perMin) - b.tokens) / rate))
	return d
}

// Sweep forgets tenants idle for longer than maxIdle, so the map cannot grow forever.
func (l *Limiter) Sweep(maxIdle time.Duration) int {
	cutoff := l.now().Add(-maxIdle)
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
			n++
		}
	}
	return n
}
