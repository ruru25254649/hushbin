package ratelimit

import (
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestBucket(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := New(3, time.Minute, c.now) // 1 token / 20 s
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d refused", i)
		}
	}
	ok, wait := l.Allow("a")
	if ok || wait != 20*time.Second {
		t.Fatalf("got %v %v", ok, wait)
	}
	if RetryAfterSeconds(wait) != 20 || RetryAfterSeconds(time.Millisecond) != 1 {
		t.Fatal("retry-after rounding")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("keys must be independent")
	}
	c.add(10 * time.Second)
	if ok, wait := l.Allow("a"); ok || wait != 10*time.Second {
		t.Fatalf("half refilled: %v %v", ok, wait)
	}
	c.add(10 * time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("should have refilled one token")
	}
	c.add(time.Hour)
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatal("burst capped at N")
		}
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("burst exceeded N")
	}
}

func TestCheckTake(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := New(2, time.Hour, c.now)
	for i := 0; i < 5; i++ {
		if ok, _ := l.Check("a"); !ok {
			t.Fatal("check must not consume")
		}
	}
	l.Take("a")
	l.Take("a")
	l.Take("a") // floors at zero
	if ok, wait := l.Check("a"); ok || wait != 30*time.Minute {
		t.Fatalf("got %v %v", ok, wait)
	}
}

func TestSweepAndNil(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	l := New(2, time.Minute, c.now)
	l.Allow("a")
	l.Allow("b")
	l.Allow("b")
	if l.Len() != 2 {
		t.Fatal(l.Len())
	}
	c.add(30 * time.Second) // a full again, b half
	if n := l.Sweep(); n != 1 || l.Len() != 1 {
		t.Fatalf("swept %d, left %d", n, l.Len())
	}
	c.add(time.Minute)
	l.Sweep()
	if l.Len() != 0 {
		t.Fatal("idle buckets not evicted")
	}

	var off *Limiter = New(0, time.Minute, nil)
	if off != nil {
		t.Fatal("disabled limiter should be nil")
	}
	if ok, _ := off.Allow("x"); !ok {
		t.Fatal("nil limiter must allow")
	}
	off.Take("x")
	off.Sweep()
}
