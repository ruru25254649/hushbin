package events

import "testing"

type ev struct {
	Type string `json:"type"`
	N    int    `json:"n"`
}

func TestPublishSubscribe(t *testing.T) {
	h := NewHub(4)
	a := h.Subscribe("p1")
	b := h.Subscribe("p1")
	other := h.Subscribe("p2")
	h.Publish("p1", "x", ev{Type: "x", N: 1})
	for _, s := range []*Subscription{a, b} {
		m := <-s.C
		if m.Type != "x" || string(m.Data) != `{"type":"x","n":1}` || m.Terminal() {
			t.Fatalf("got %+v", m)
		}
	}
	select {
	case m := <-other.C:
		t.Fatalf("leaked event %+v", m)
	default:
	}
	a.Close()
	a.Close() // idempotent
	if _, open := <-a.C; open {
		t.Fatal("closed subscription channel still open")
	}
	if h.Subscribers("p1") != 1 {
		t.Fatal(h.Subscribers("p1"))
	}
	h.Publish("p1", PasteDeleted, ev{Type: PasteDeleted})
	if m := <-b.C; !m.Terminal() {
		t.Fatal("paste.deleted must be terminal")
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	h := NewHub(2)
	s := h.Subscribe("p")
	for i := 0; i < 3; i++ {
		h.Publish("p", "x", ev{Type: "x", N: i})
	}
	n := 0
	for range s.C {
		n++
	}
	if n != 2 || h.Subscribers("p") != 0 {
		t.Fatalf("received %d, subscribers %d", n, h.Subscribers("p"))
	}
	s.Close()
}

func TestClose(t *testing.T) {
	h := NewHub(2)
	s := h.Subscribe("p")
	h.Close()
	if _, open := <-s.C; open {
		t.Fatal("hub close must close subscriptions")
	}
	late := h.Subscribe("p")
	if _, open := <-late.C; open {
		t.Fatal("subscriptions after close must be closed")
	}
	h.Publish("p", "x", ev{Type: "x"})
}
