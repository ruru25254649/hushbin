package api_test

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"hushbin/server/config"
)

func TestCommentsCreate(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.MaxComment = 400; c.MaxComments = 6 })
	c := e.create(nil)
	path := "/api/pastes/" + c.ID + "/comments"
	guest := randBytes(32)
	guestHdr := merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(guest)})

	now := e.clock.Now().UnixMilli()
	gc, content := e.comment(c, guest, false, nil)
	if gc.Author != "guest" || gc.Tag != tagOf(guest) || gc.ParentID != nil || gc.Deleted || gc.Version != 1 ||
		gc.Content == nil || *gc.Content != b64(content) || gc.CreatedAt != now || gc.UpdatedAt != now {
		t.Fatalf("guest comment %+v", gc)
	}
	oc, _ := e.comment(c, nil, true, nil)
	if oc.Author != "owner" || oc.Tag != tagOf(c.keys.owner) {
		t.Fatalf("owner comment %+v", oc)
	}
	// Owner token wins when both are sent.
	var both commentT
	e.expectJSON(e.doJSON("POST", path, merge(guestHdr, ownerHdr(c.keys)),
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 201, &both)
	if both.Author != "owner" {
		t.Fatalf("expected owner author, got %+v", both)
	}

	// One level of replies.
	reply, _ := e.comment(c, randBytes(32), false, &gc.ID)
	if reply.ParentID == nil || *reply.ParentID != gc.ID {
		t.Fatalf("reply %+v", reply)
	}
	e.expectErr(e.doJSON("POST", path, guestHdr, map[string]any{"id": newID(), "parentId": reply.ID, "content": b64(envelope(5))}), 400, "invalid")
	e.expectErr(e.doJSON("POST", path, guestHdr, map[string]any{"id": newID(), "parentId": newID(), "content": b64(envelope(5))}), 400, "invalid")

	// Identity and auth.
	e.expectErr(e.doJSON("POST", path, readHdr(c.keys), map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 403, "forbidden")
	e.expectErr(e.doJSON("POST", path, merge(readHdr(c.keys), map[string]string{"X-Author-Token": "short"}),
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 403, "forbidden")
	e.expectErr(e.doJSON("POST", path, merge(readHdr(c.keys), map[string]string{"X-Owner-Token": b64(guest)}),
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 403, "forbidden")
	e.expectErr(e.doJSON("POST", path, map[string]string{"X-Author-Token": b64(guest)},
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 403, "unauthorized")

	// Validation.
	e.expectErr(e.doJSON("POST", path, guestHdr, map[string]any{"id": gc.ID, "parentId": nil, "content": b64(envelope(5))}), 409, "conflict")
	e.expectErr(e.doJSON("POST", path, guestHdr, map[string]any{"id": "tooshort", "parentId": nil, "content": b64(envelope(5))}), 400, "invalid")
	e.expectErr(e.doJSON("POST", path, guestHdr, map[string]any{"id": newID(), "parentId": "bad", "content": b64(envelope(5))}), 400, "invalid")
	e.expectErr(e.doJSON("POST", path, guestHdr, map[string]any{"id": newID(), "parentId": nil}), 400, "invalid")
	e.expectErr(e.doJSON("POST", path, guestHdr, map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(400))}), 413, "too_large")

	// HB_MAX_COMMENTS (4 so far: gc, oc, both, reply).
	e.comment(c, guest, false, nil)
	e.comment(c, guest, false, nil)
	e.expectErr(e.doJSON("POST", path, guestHdr, map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 413, "too_large")

	// Discussion disabled.
	nd := e.create(func(m map[string]any) { m["discussion"] = false })
	e.expectErr(e.doJSON("POST", "/api/pastes/"+nd.ID+"/comments", merge(readHdr(nd.keys), ownerHdr(nd.keys)),
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 403, "locked")

	// Open lists comments in creation order.
	o := e.open(c)
	if len(o.Comments) != 6 || o.Comments[0].ID != gc.ID || o.Comments[1].ID != oc.ID || o.Comments[3].ID != reply.ID {
		t.Fatalf("comments in open: %+v", o.Comments)
	}
}

func TestCommentsEdit(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.MaxVersions = 3 })
	c := e.create(nil)
	guest, other := randBytes(32), randBytes(32)
	gc, first := e.comment(c, guest, false, nil)
	oc, _ := e.comment(c, nil, true, nil)
	path := "/api/pastes/" + c.ID + "/comments/"
	guestHdr := merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(guest)})
	otherHdr := merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(other)})
	ownerH := merge(readHdr(c.keys), ownerHdr(c.keys))

	body := func(base int) map[string]any { return map[string]any{"content": b64(envelope(8)), "baseVersion": base} }
	// Only the author may edit.
	e.expectErr(e.doJSON("PUT", path+gc.ID, otherHdr, body(1)), 403, "forbidden")
	e.expectErr(e.doJSON("PUT", path+gc.ID, ownerH, body(1)), 403, "forbidden")
	e.expectErr(e.doJSON("PUT", path+gc.ID, readHdr(c.keys), body(1)), 403, "forbidden")
	e.expectErr(e.doJSON("PUT", path+oc.ID, guestHdr, body(1)), 403, "forbidden")
	e.expectErr(e.doJSON("PUT", path+gc.ID, map[string]string{"X-Author-Token": b64(guest)}, body(1)), 403, "unauthorized")
	e.expectErr(e.doJSON("PUT", path+newID(), guestHdr, body(1)), 404, "not_found")
	e.expectErr(e.doJSON("PUT", path+"nope", guestHdr, body(1)), 404, "not_found")
	e.expectErr(e.doJSON("PUT", path+gc.ID, guestHdr, map[string]any{"content": b64(envelope(8))}), 400, "invalid")

	e.clock.Advance(2 * time.Second)
	second := envelope(9)
	var up commentT
	e.expectJSON(e.doJSON("PUT", path+gc.ID, guestHdr, map[string]any{"content": b64(second), "baseVersion": 1}), 200, &up)
	if up.Version != 2 || *up.Content != b64(second) || up.UpdatedAt != e.clock.Now().UnixMilli() ||
		up.CreatedAt != gc.CreatedAt || up.Tag != gc.Tag || up.Author != "guest" {
		t.Fatalf("edited %+v", up)
	}
	ae := e.expectErr(e.doJSON("PUT", path+gc.ID, guestHdr, body(1)), 409, "conflict")
	if ae.Version == nil || *ae.Version != 2 {
		t.Fatalf("conflict version %+v", ae)
	}
	// Owner edits own comment.
	var oup commentT
	e.expectJSON(e.doJSON("PUT", path+oc.ID, ownerH, body(1)), 200, &oup)
	if oup.Version != 2 || oup.Author != "owner" {
		t.Fatalf("owner edit %+v", oup)
	}

	// History.
	var hist []struct {
		Version   int    `json:"version"`
		CreatedAt int64  `json:"createdAt"`
		Content   string `json:"content"`
	}
	e.expectErr(e.do("GET", path+gc.ID+"/versions", nil, nil), 403, "unauthorized")
	e.expectJSON(e.do("GET", path+gc.ID+"/versions", readHdr(c.keys), nil), 200, &hist)
	if len(hist) != 2 || hist[0].Version != 1 || hist[0].Content != b64(first) || hist[1].Version != 2 ||
		hist[1].Content != b64(second) || hist[0].CreatedAt != gc.CreatedAt {
		t.Fatalf("history %+v", hist)
	}
	e.expectErr(e.do("GET", path+newID()+"/versions", readHdr(c.keys), nil), 404, "not_found")

	// Version cap (3).
	e.expectJSON(e.doJSON("PUT", path+gc.ID, guestHdr, body(2)), 200, nil)
	e.expectErr(e.doJSON("PUT", path+gc.ID, guestHdr, body(3)), 413, "too_large")
}

func TestCommentsDeleteAndTombstones(t *testing.T) {
	e := newEnv(t, nil)
	c := e.create(nil)
	base := "/api/pastes/" + c.ID + "/comments/"
	alice, bob, carol := randBytes(32), randBytes(32), randBytes(32)
	hdr := func(tok []byte) map[string]string {
		return merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(tok)})
	}
	del := func(id string, h map[string]string) *http.Response { return e.do("DELETE", base+id, h, nil) }
	expect204 := func(resp *http.Response) {
		t.Helper()
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Fatalf("status %d, want 204", resp.StatusCode)
		}
	}

	top, _ := e.comment(c, alice, false, nil)
	r1, _ := e.comment(c, bob, false, &top.ID)
	r2, _ := e.comment(c, carol, false, &top.ID)
	lone, _ := e.comment(c, bob, false, nil)
	// Attachments on the top comment and on a reply.
	topAtt, r1Att := newID(), newID()
	e.expectJSON(e.upload(c, readHdr(c.keys), topAtt, &top.ID, envelope(50)), 201, nil)
	e.expectJSON(e.upload(c, readHdr(c.keys), r1Att, &r1.ID, envelope(50)), 201, nil)

	// Authorization.
	e.expectErr(del(top.ID, hdr(bob)), 403, "forbidden")
	e.expectErr(del(top.ID, readHdr(c.keys)), 403, "forbidden")
	e.expectErr(del(top.ID, map[string]string{"X-Author-Token": b64(alice)}), 403, "unauthorized")
	e.expectErr(del(newID(), hdr(alice)), 404, "not_found")

	// A top-level comment without replies is removed outright.
	expect204(del(lone.ID, hdr(bob)))

	// With replies it becomes a tombstone; its history and attachments go.
	expect204(del(top.ID, hdr(alice)))
	o := e.open(c)
	if len(o.Comments) != 3 {
		t.Fatalf("comments %+v", o.Comments)
	}
	tomb := o.Comments[0]
	if tomb.ID != top.ID || !tomb.Deleted || tomb.Content != nil || tomb.Tag != top.Tag {
		t.Fatalf("tombstone %+v", tomb)
	}
	if exists(e.blobPath(c.ID, topAtt)) {
		t.Fatal("tombstoned comment's attachment blob survived")
	}
	for _, a := range o.Attachments {
		if a.ID == topAtt {
			t.Fatal("tombstoned comment's attachment still listed")
		}
	}
	e.expectErr(e.do("GET", base+top.ID+"/versions", readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.doJSON("PUT", base+top.ID, hdr(alice), map[string]any{"content": b64(envelope(5)), "baseVersion": 1}), 400, "invalid")
	e.expectErr(del(top.ID, hdr(alice)), 404, "not_found")
	e.expectErr(e.doJSON("POST", "/api/pastes/"+c.ID+"/comments", hdr(bob),
		map[string]any{"id": newID(), "parentId": top.ID, "content": b64(envelope(5))}), 400, "invalid")

	// Owner moderation removes r1 (and its attachment); the tombstone stays
	// while r2 remains.
	expect204(del(r1.ID, merge(readHdr(c.keys), ownerHdr(c.keys))))
	if exists(e.blobPath(c.ID, r1Att)) {
		t.Fatal("reply attachment survived")
	}
	o = e.open(c)
	if len(o.Comments) != 2 || o.Comments[0].ID != top.ID || o.Comments[1].ID != r2.ID {
		t.Fatalf("after moderation %+v", o.Comments)
	}
	// Removing the last reply removes the tombstone too.
	expect204(del(r2.ID, hdr(carol)))
	if o = e.open(c); len(o.Comments) != 0 || len(o.Attachments) != 0 {
		t.Fatalf("expected empty discussion, got %+v / %+v", o.Comments, o.Attachments)
	}

	// The owner may delete anyone's comment but guests cannot delete the owner's.
	oc, _ := e.comment(c, nil, true, nil)
	e.expectErr(del(oc.ID, hdr(alice)), 403, "forbidden")
	expect204(del(oc.ID, merge(readHdr(c.keys), ownerHdr(c.keys))))
}

func TestAttachments(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.MaxAttachment = 1000
		c.MaxPasteAttachments = 1500
	})
	c := e.create(nil, newAtt(200))
	ownerH := merge(readHdr(c.keys), ownerHdr(c.keys))

	// Paste attachments need the owner token (and read auth).
	e.expectErr(e.upload(c, readHdr(c.keys), newID(), nil, envelope(10)), 403, "forbidden")
	e.expectErr(e.upload(c, ownerHdr(c.keys), newID(), nil, envelope(10)), 403, "unauthorized")
	e.expectErr(e.upload(c, merge(readHdr(c.keys), map[string]string{"X-Owner-Token": b64(randBytes(32))}), newID(), nil, envelope(10)), 403, "forbidden")

	data := envelope(300)
	id := newID()
	var a attachmentT
	e.expectJSON(e.upload(c, ownerH, id, nil, data), 201, &a)
	if a.ID != id || a.CommentID != nil || a.Size != int64(len(data)) || a.Data != nil || len(unb64(t, a.Meta)) != 50 {
		t.Fatalf("attachment %+v", a)
	}
	e.expectErr(e.upload(c, ownerH, id, nil, envelope(10)), 409, "conflict")
	if b, _ := os.ReadFile(e.blobPath(c.ID, id)); !bytes.Equal(b, data) {
		t.Fatal("duplicate upload overwrote the original blob")
	}

	// Comment attachments: any reader may upload (discussion on, unlocked),
	// even before the comment exists (clients upload first, then post).
	cid := newID()
	var ca attachmentT
	e.expectJSON(e.upload(c, readHdr(c.keys), newID(), &cid, envelope(10)), 201, &ca)
	if ca.CommentID == nil || *ca.CommentID != cid {
		t.Fatalf("comment attachment %+v", ca)
	}

	// Download.
	path := "/api/pastes/" + c.ID + "/attachments/" + id
	resp := e.do("GET", path, readHdr(c.keys), nil)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !bytes.Equal(got, data) || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("download: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	e.expectErr(e.do("GET", path, nil, nil), 403, "unauthorized")
	e.expectErr(e.do("GET", path, map[string]string{"X-Read-Auth": b64(randBytes(32))}, nil), 403, "unauthorized")
	e.expectErr(e.do("GET", path, ownerHdr(c.keys), nil), 403, "unauthorized")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/attachments/"+newID(), readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/attachments/..%2f..%2fhushbin.db", readHdr(c.keys), nil), 404, "not_found")

	// Limits: per attachment, then per paste (230+330+40 = 600 of 1500 used).
	e.expectErr(e.upload(c, ownerH, newID(), nil, envelope(1000)), 413, "too_large")
	e.expectJSON(e.upload(c, ownerH, newID(), nil, envelope(860)), 201, nil)
	e.expectErr(e.upload(c, ownerH, newID(), nil, envelope(100)), 413, "too_large")

	// Validation.
	e.expectErr(e.upload(c, ownerH, "bad", nil, envelope(5)), 400, "invalid")
	e.expectErr(e.upload(c, ownerH, newID(), nil, []byte("not an envelope at all, no no no")), 400, "invalid")
	bad := "x"
	e.expectErr(e.upload(c, readHdr(c.keys), newID(), &bad, envelope(5)), 400, "invalid")

	// Discussion disabled: no comment attachments.
	nd := e.create(func(m map[string]any) { m["discussion"] = false })
	e.expectErr(e.upload(nd, merge(readHdr(nd.keys), ownerHdr(nd.keys)), newID(), &cid, envelope(5)), 403, "locked")

	// Open lists all attachments, with comment links.
	o := e.open(c)
	if len(o.Attachments) != 4 || o.Attachments[2].CommentID == nil || *o.Attachments[2].CommentID != cid {
		t.Fatalf("open attachments %+v", o.Attachments)
	}
}
