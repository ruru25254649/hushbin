// Package events is an in-process publish/subscribe hub keyed by paste ID,
// feeding the SSE endpoint (DESIGN.md §4 "Live events").
package events

import (
	"encoding/json"
	"sync"
)

// Event types.
const (
	CommentCreated    = "comment.created"
	CommentUpdated    = "comment.updated"
	CommentDeleted    = "comment.deleted"
	AttachmentCreated = "attachment.created"
	PasteUpdated      = "paste.updated"
	PasteLocked       = "paste.locked"
	PasteDeleted      = "paste.deleted"
)

// Message is one serialized event.
type Message struct {
	Type string
	Data []byte // JSON object, one line
}

// Terminal reports whether the stream ends after this message.
func (m Message) Terminal() bool { return m.Type == PasteDeleted }

// Subscription receives messages for one paste. C is closed when the
// subscription is cancelled, the subscriber falls too far behind, or the hub
// shuts down.
type Subscription struct {
	C <-chan Message

	ch     chan Message
	hub    *Hub
	paste  string
	closed bool // guarded by hub.mu
}

// Close unsubscribes. It is safe to call more than once.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	s.hub.removeLocked(s)
}

// Hub fans out messages to subscribers.
type Hub struct {
	mu     sync.Mutex
	subs   map[string]map[*Subscription]struct{}
	buffer int
	closed bool
}

// NewHub returns a hub whose subscribers buffer up to buffer messages.
func NewHub(buffer int) *Hub {
	if buffer <= 0 {
		buffer = 64
	}
	return &Hub{subs: make(map[string]map[*Subscription]struct{}), buffer: buffer}
}

// Subscribe registers a subscriber for pasteID.
func (h *Hub) Subscribe(pasteID string) *Subscription {
	ch := make(chan Message, h.buffer)
	s := &Subscription{C: ch, ch: ch, hub: h, paste: pasteID}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.closed = true
		close(ch)
		return s
	}
	m := h.subs[pasteID]
	if m == nil {
		m = make(map[*Subscription]struct{})
		h.subs[pasteID] = m
	}
	m[s] = struct{}{}
	return s
}

func (h *Hub) removeLocked(s *Subscription) {
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
	if m := h.subs[s.paste]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(h.subs, s.paste)
		}
	}
}

// Publish sends ev (marshalled as JSON; it must carry a "type" field equal
// to typ) to every subscriber of pasteID. Subscribers whose buffer is full
// are disconnected rather than blocking the publisher.
func (h *Hub) Publish(pasteID, typ string, ev any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs[pasteID]) == 0 {
		return
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	msg := Message{Type: typ, Data: data}
	for s := range h.subs[pasteID] {
		select {
		case s.ch <- msg:
		default:
			h.removeLocked(s)
		}
	}
}

// Subscribers returns the number of subscribers for pasteID.
func (h *Hub) Subscribers(pasteID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[pasteID])
}

// Close disconnects every subscriber and rejects new ones. Used on shutdown.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for _, m := range h.subs {
		for s := range m {
			h.removeLocked(s)
		}
	}
}
