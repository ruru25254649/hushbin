package api_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hushbin/server/api"
	"hushbin/server/config"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEvents(t *testing.T) {
	e := newEnv(t, nil)
	c := e.create(nil)
	guest := randBytes(32)

	// Auth.
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/events", nil, nil), 403, "unauthorized")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/events", map[string]string{"X-Read-Auth": b64(randBytes(32))}, nil), 403, "unauthorized")
	e.expectErr(e.do("GET", "/api/pastes/AAAAAAAAAA/events", readHdr(c.keys), nil), 404, "not_found")

	s := e.subscribe(c)
	h := s.resp.Header
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-cache" ||
		h.Get("X-Accel-Buffering") != "no" || h.Get("Content-Security-Policy") == "" {
		t.Fatalf("event stream headers %v", h)
	}
	waitFor(t, "subscriber", func() bool { return e.srv.Hub().Subscribers(c.ID) == 1 })

	gc, content := e.comment(c, guest, false, nil)
	ev := s.next()
	if ev["type"] != "comment.created" {
		t.Fatalf("event %v", ev)
	}
	cm := ev["comment"].(map[string]any)
	if cm["id"] != gc.ID || cm["author"] != "guest" || cm["content"] != b64(content) || cm["parentId"] != nil ||
		cm["deleted"] != false || cm["version"].(float64) != 1 || cm["tag"] != tagOf(guest) {
		t.Fatalf("comment.created payload %v", cm)
	}

	reply, _ := e.comment(c, nil, true, &gc.ID)
	if ev = s.next(); ev["type"] != "comment.created" {
		t.Fatalf("event %v", ev)
	}

	e.expectJSON(e.doJSON("PUT", "/api/pastes/"+c.ID+"/comments/"+gc.ID,
		merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(guest)}),
		map[string]any{"content": b64(envelope(5)), "baseVersion": 1}), 200, nil)
	if ev = s.next(); ev["type"] != "comment.updated" || ev["comment"].(map[string]any)["version"].(float64) != 2 {
		t.Fatalf("event %v", ev)
	}

	aid := newID()
	e.expectJSON(e.upload(c, readHdr(c.keys), aid, &gc.ID, envelope(10)), 201, nil)
	if ev = s.next(); ev["type"] != "attachment.created" || ev["attachment"].(map[string]any)["id"] != aid ||
		ev["attachment"].(map[string]any)["commentId"] != gc.ID {
		t.Fatalf("event %v", ev)
	}

	var up struct {
		Version   int   `json:"version"`
		UpdatedAt int64 `json:"updatedAt"`
	}
	e.expectJSON(e.doJSON("PUT", "/api/pastes/"+c.ID, ownerHdr(c.keys), map[string]any{"content": b64(envelope(5)), "baseVersion": 1}), 200, &up)
	if ev = s.next(); ev["type"] != "paste.updated" || ev["version"].(float64) != 2 || ev["updatedAt"].(float64) != float64(up.UpdatedAt) {
		t.Fatalf("event %v", ev)
	}

	e.expectJSON(e.doJSON("POST", "/api/pastes/"+c.ID+"/lock", ownerHdr(c.keys), map[string]any{"locked": true}), 200, nil)
	if ev = s.next(); ev["type"] != "paste.locked" || ev["locked"] != true {
		t.Fatalf("event %v", ev)
	}

	// Tombstoning is an update; removing the last reply deletes both.
	resp := e.do("DELETE", "/api/pastes/"+c.ID+"/comments/"+gc.ID, merge(readHdr(c.keys), ownerHdr(c.keys)), nil)
	resp.Body.Close()
	if ev = s.next(); ev["type"] != "comment.updated" || ev["comment"].(map[string]any)["deleted"] != true ||
		ev["comment"].(map[string]any)["content"] != nil {
		t.Fatalf("event %v", ev)
	}
	resp = e.do("DELETE", "/api/pastes/"+c.ID+"/comments/"+reply.ID, merge(readHdr(c.keys), ownerHdr(c.keys)), nil)
	resp.Body.Close()
	if ev = s.next(); ev["type"] != "comment.deleted" || ev["id"] != reply.ID {
		t.Fatalf("event %v", ev)
	}
	if ev = s.next(); ev["type"] != "comment.deleted" || ev["id"] != gc.ID {
		t.Fatalf("event %v", ev)
	}

	// A second subscriber, then destroy: both get paste.deleted and the
	// streams end.
	s2 := e.subscribe(c)
	waitFor(t, "second subscriber", func() bool { return e.srv.Hub().Subscribers(c.ID) == 2 })
	resp = e.do("DELETE", "/api/pastes/"+c.ID, ownerHdr(c.keys), nil)
	resp.Body.Close()
	for _, sc := range []*sseClient{s, s2} {
		if ev = sc.next(); ev["type"] != "paste.deleted" || len(ev) != 1 {
			t.Fatalf("event %v", ev)
		}
		sc.expectClosed()
	}
	waitFor(t, "subscriber cleanup", func() bool { return e.srv.Hub().Subscribers(c.ID) == 0 })
}

func TestEventsPingAndDisconnect(t *testing.T) {
	e := newEnv(t, nil, func(o *api.Options) { o.PingInterval = 20 * time.Millisecond })
	c := e.create(nil)
	s := e.subscribe(c)
	pings := 0
	timeout := time.After(5 * time.Second)
	for pings < 3 {
		select {
		case ev := <-s.events:
			if ev == "PING" {
				pings++
			}
		case <-timeout:
			t.Fatal("no pings")
		}
	}
	waitFor(t, "subscriber", func() bool { return e.srv.Hub().Subscribers(c.ID) == 1 })
	s.resp.Body.Close()
	waitFor(t, "subscriber removal after disconnect", func() bool { return e.srv.Hub().Subscribers(c.ID) == 0 })
}

func TestEventsStreamLimitPerIP(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.MaxStreamsPerIP = 2 })
	c := e.create(nil)
	s1 := e.subscribe(c)
	e.subscribe(c)
	resp := e.do("GET", "/api/pastes/"+c.ID+"/events", readHdr(c.keys), nil)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After missing")
	}
	e.expectErr(resp, http.StatusTooManyRequests, "rate_limited")
	s1.resp.Body.Close()
	waitFor(t, "stream slot release", func() bool { return e.srv.Hub().Subscribers(c.ID) == 1 })
	e.subscribe(c)
}

func TestExpirySweep(t *testing.T) {
	e := newEnv(t, nil)
	a := newAtt(100)
	c := e.create(func(m map[string]any) { m["expiresIn"] = 300 }, a)
	keep := e.create(func(m map[string]any) { m["expiresIn"] = 3600 })
	forever := e.create(func(m map[string]any) { m["expiresIn"] = 0 })
	s := e.subscribe(c)
	waitFor(t, "subscriber", func() bool { return e.srv.Hub().Subscribers(c.ID) == 1 })

	e.clock.Advance(299 * time.Second)
	e.expectJSON(e.do("GET", "/api/pastes/"+c.ID, nil, nil), 200, nil)
	e.clock.Advance(1 * time.Second) // exactly at expiresAt: expired

	// Expired pastes are not found everywhere, even before the sweep.
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID, nil, nil), 404, "not_found")
	e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/attachments/"+a.id, readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("DELETE", "/api/pastes/"+c.ID, ownerHdr(c.keys), nil), 404, "not_found")
	if !exists(e.blobPath(c.ID, a.id)) {
		t.Fatal("blob removed before sweep")
	}

	e.srv.Sweep(context.Background())
	if ev := s.next(); ev["type"] != "paste.deleted" {
		t.Fatalf("event %v", ev)
	}
	s.expectClosed()
	if exists(filepath.Join(e.dir, "att", c.ID)) {
		t.Fatal("expired blobs not removed")
	}
	if _, err := e.store.GetPaste(context.Background(), c.ID); err == nil {
		t.Fatal("expired paste still readable from store")
	}
	// Others are untouched.
	e.expectJSON(e.do("GET", "/api/pastes/"+keep.ID, nil, nil), 200, nil)
	e.expectJSON(e.do("GET", "/api/pastes/"+forever.ID, nil, nil), 200, nil)

	e.clock.Advance(100 * 24 * time.Hour)
	e.srv.Sweep(context.Background())
	e.expectErr(e.do("GET", "/api/pastes/"+keep.ID, nil, nil), 404, "not_found")
	e.expectJSON(e.do("GET", "/api/pastes/"+forever.ID, nil, nil), 200, nil)
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "att")); len(entries) != 0 {
		t.Fatalf("att/ not empty: %d", len(entries))
	}
}
