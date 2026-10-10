// Package ratelimit implements keyed token-bucket rate limiters.
package ratelimit

import (
	"math"
	"sync"
	"time"
)

// Limiter is a set of token buckets keyed by string (client IP). Each bucket
// holds up to burst tokens and refills at n tokens per period. A nil
// *Limiter allows everything.
type Limiter struct {
	ratePerSec float64
	burst      float64
	now        func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter allowing n events per period with a burst of n.
// It returns nil (allow everything) when n <= 0 or per <= 0.
// now may be nil (time.Now).
func New(n int, per time.Duration, now func() time.Time) *Limiter {
	if n <= 0 || per <= 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		ratePerSec: float64(n) / per.Seconds(),
		burst:      float64(n),
		now:        now,
		buckets:    make(map[string]*bucket),
	}
}

// refill returns the bucket for key, topped up to the current time.
// l.mu must be held.
func (l *Limiter) refill(key string, now time.Time) *bucket {
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
		return b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = math.Min(l.burst, b.tokens+el*l.ratePerSec)
		b.last = now
	}
	return b
}

func (l *Limiter) wait(b *bucket) time.Duration {
	missing := 1 - b.tokens
	if missing <= 0 {
		return 0
	}
	return time.Duration(missing / l.ratePerSec * float64(time.Second))
}

// Allow consumes one token for key. If none is available it returns false
// and how long until one will be.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, l.now())
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, l.wait(b)
}

// Check reports whether key has a token available without consuming it.
func (l *Limiter) Check(key string) (bool, time.Duration) {
	if l == nil {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, l.now())
	if b.tokens >= 1 {
		return true, 0
	}
	return false, l.wait(b)
}

// Take consumes one token for key if available (used to charge failures
// after the fact).
func (l *Limiter) Take(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.refill(key, l.now())
	if b.tokens >= 1 {
		b.tokens--
	} else {
		b.tokens = 0
	}
}

// Sweep removes buckets that have refilled completely (they are
// indistinguishable from fresh ones). It returns the number removed.
func (l *Limiter) Sweep() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	n := 0
	for k, b := range l.buckets {
		full := b.tokens + now.Sub(b.last).Seconds()*l.ratePerSec
		if full >= l.burst {
			delete(l.buckets, k)
			n++
		}
	}
	return n
}

// Len returns the number of tracked buckets.
func (l *Limiter) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// RetryAfterSeconds converts a wait duration to a Retry-After value (>= 1).
func RetryAfterSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}
