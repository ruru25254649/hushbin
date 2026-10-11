package api

import (
	"bufio"
	"net/http"
	"time"
)

// GET /api/pastes/{id}/events — Server-Sent Events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := s.readableNonBurn(w, r)
	if !ok {
		return
	}
	key := info(r).key
	if !s.acquireStream(key) {
		rateLimited(w, 30*time.Second)
		return
	}
	sub := s.hub.Subscribe(p.ID)
	defer func() {
		s.releaseStream(key)
		sub.Close()
	}()
	// The paste may have been deleted between the lookup and Subscribe, in
	// which case its paste.deleted event was missed: check again.
	if _, err := s.store.GetPaste(r.Context(), p.ID); err != nil {
		s.fail(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	bw := bufio.NewWriter(w)
	send := func(chunks ...[]byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(streamWriteWait))
		for _, c := range chunks {
			bw.Write(c)
		}
		if err := bw.Flush(); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	ping := []byte(": ping\n\n")
	if !send(ping) {
		return
	}
	tick := time.NewTicker(s.ping)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if !send(ping) {
				return
			}
		case msg, open := <-sub.C:
			if !open {
				return // hub shut down or we fell behind; the client reconnects
			}
			if !send([]byte("data: "), msg.Data, []byte("\n\n")) {
				return
			}
			if msg.Terminal() {
				return
			}
		}
	}
}
