package api

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

func TestTicketStore(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	ts := newTicketStore(3, 15*time.Minute, clock)
	ref := bytes.Repeat([]byte{7}, 32)
	sec := ts.mint("AAAAAAAAAA", [][]byte{ref})
	if len(sec) != 32 {
		t.Fatal(len(sec))
	}
	tk := ts.lookup("AAAAAAAAAA", sec)
	if tk == nil || !tk.has(ref) || tk.has(bytes.Repeat([]byte{8}, 32)) || tk.has([]byte{7}) {
		t.Fatal("lookup/has")
	}
	if ts.lookup("BBBBBBBBBB", sec) != nil {
		t.Fatal("ticket must be bound to its paste")
	}
	wrong := append([]byte(nil), sec...)
	wrong[31] ^= 1
	if ts.lookup("AAAAAAAAAA", wrong) != nil || ts.lookup("AAAAAAAAAA", sec[:31]) != nil {
		t.Fatal("wrong secret accepted")
	}

	// Expiry: lazily on lookup, and by sweep.
	advance(15 * time.Minute)
	if ts.lookup("AAAAAAAAAA", sec) != nil || ts.len() != 0 {
		t.Fatal("expired ticket accepted / kept")
	}
	ts.mint("CCCCCCCCCC", nil)
	advance(16 * time.Minute)
	ts.sweep()
	if ts.len() != 0 {
		t.Fatal("sweep kept expired ticket")
	}

	// Bounded: the oldest ticket is evicted.
	s1 := ts.mint("DDDDDDDDDD", nil)
	s2 := ts.mint("DDDDDDDDDD", nil)
	ts.mint("EEEEEEEEEE", nil)
	ts.mint("FFFFFFFFFF", nil)
	if ts.len() != 3 || ts.lookup("DDDDDDDDDD", s1) != nil || ts.lookup("DDDDDDDDDD", s2) == nil {
		t.Fatalf("eviction: len=%d", ts.len())
	}
}
