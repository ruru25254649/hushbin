package api

import (
	"container/list"
	"crypto/rand"
	"crypto/subtle"
	"sync"
	"time"
)

const (
	ticketTTL        = 15 * time.Minute
	maxImageTickets  = 10000
	imageTicketBytes = 32
)

// imageTicket lets the reader of a burned paste fetch its registered images
// after the paste row is gone. It is bound to the paste id and a snapshot of
// the paste's image-ref set.
type imageTicket struct {
	pasteID string
	secret  [imageTicketBytes]byte
	refs    map[[32]byte]struct{}
	expires time.Time
	elem    *list.Element
}

func (t *imageTicket) has(ref []byte) bool {
	if len(ref) != 32 {
		return false
	}
	_, ok := t.refs[[32]byte(ref)]
	return ok
}

// ticketStore holds image tickets in memory, bounded in number: when full,
// the oldest ticket is evicted.
type ticketStore struct {
	mu      sync.Mutex
	byPaste map[string][]*imageTicket
	order   *list.List // oldest first
	max     int
	ttl     time.Duration
	now     func() time.Time
}

func newTicketStore(max int, ttl time.Duration, now func() time.Time) *ticketStore {
	return &ticketStore{byPaste: map[string][]*imageTicket{}, order: list.New(), max: max, ttl: ttl, now: now}
}

// mint creates a ticket for pasteID allowing refs and returns its secret.
func (ts *ticketStore) mint(pasteID string, refs [][]byte) []byte {
	t := &imageTicket{pasteID: pasteID, refs: make(map[[32]byte]struct{}, len(refs))}
	if _, err := rand.Read(t.secret[:]); err != nil {
		panic(err)
	}
	for _, r := range refs {
		if len(r) == 32 {
			t.refs[[32]byte(r)] = struct{}{}
		}
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	t.expires = ts.now().Add(ts.ttl)
	for ts.order.Len() >= ts.max {
		ts.removeLocked(ts.order.Front().Value.(*imageTicket))
	}
	t.elem = ts.order.PushBack(t)
	ts.byPaste[pasteID] = append(ts.byPaste[pasteID], t)
	return append([]byte(nil), t.secret[:]...)
}

// lookup returns the live ticket for pasteID matching secret (compared in
// constant time), or nil.
func (ts *ticketStore) lookup(pasteID string, secret []byte) *imageTicket {
	if len(secret) != imageTicketBytes {
		return nil
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := ts.now()
	var found *imageTicket
	for _, t := range ts.byPaste[pasteID] {
		if subtle.ConstantTimeCompare(t.secret[:], secret) == 1 {
			found = t
		}
	}
	if found == nil {
		return nil
	}
	if !now.Before(found.expires) {
		ts.removeLocked(found)
		return nil
	}
	return found
}

func (ts *ticketStore) removeLocked(t *imageTicket) {
	ts.order.Remove(t.elem)
	same := ts.byPaste[t.pasteID]
	for i, x := range same {
		if x == t {
			same = append(same[:i], same[i+1:]...)
			break
		}
	}
	if len(same) == 0 {
		delete(ts.byPaste, t.pasteID)
	} else {
		ts.byPaste[t.pasteID] = same
	}
}

// sweep removes expired tickets.
func (ts *ticketStore) sweep() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := ts.now()
	for el := ts.order.Front(); el != nil; {
		next := el.Next()
		if t := el.Value.(*imageTicket); !now.Before(t.expires) {
			ts.removeLocked(t)
		}
		el = next
	}
}

func (ts *ticketStore) len() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.order.Len()
}
