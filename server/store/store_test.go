package store

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func rnd(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func open(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	dir := t.TempDir()
	c := &clock{t: time.UnixMilli(1_760_000_000_000)}
	s, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, c, dir
}

func newPaste(burn bool, expiresIn int64) NewPaste {
	return NewPaste{
		Burn: burn, Discussion: !burn, ExpiresIn: expiresIn,
		Salt: rnd(16), ReadVerifier: rnd(32), OwnerVerifier: rnd(32), Content: rnd(40),
	}
}

func blob(t *testing.T, s *Store, n int) NewBlob {
	t.Helper()
	f, err := s.TempFile()
	if err != nil {
		t.Fatal(err)
	}
	f.Write(rnd(n))
	f.Close()
	id := make([]byte, 22)
	for i := range id {
		id[i] = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"[rnd(1)[0]%64]
	}
	return NewBlob{ID: string(id), Meta: rnd(40), TmpPath: f.Name(), Size: int64(n)}
}

func TestPasteIDs(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id := NewPasteID()
		if !ValidPasteID(id) || seen[id] {
			t.Fatalf("bad or duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestReopenAndWAL(t *testing.T) {
	s, c, dir := open(t)
	p, err := s.CreatePaste(context.Background(), newPaste(false, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(dir, Options{Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	var mode string
	if err := s2.r.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode %q %v", mode, err)
	}
	var fk int
	if err := s2.w.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys %d %v", fk, err)
	}
	if _, err := s2.GetPaste(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	// Readers cannot write.
	if _, err := s2.r.Exec("DELETE FROM pastes"); err == nil {
		t.Fatal("reader pool must be query-only")
	}
}

func TestBurnExactlyOnce(t *testing.T) {
	s, _, dir := open(t)
	ctx := context.Background()
	p, err := s.CreatePaste(ctx, newPaste(true, 3600), []NewBlob{blob(t, s, 100), blob(t, s, 200)})
	if err != nil {
		t.Fatal(err)
	}
	var wins, notFound atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := s.Burn(ctx, p.ID)
			if err == nil {
				wins.Add(1)
				for _, a := range b.Attachments {
					if st, err := os.Stat(b.BlobPath(a.ID)); err != nil || st.Size() != a.Size {
						t.Errorf("burned blob unavailable: %v", err)
					}
				}
				b.Cleanup()
				return
			}
			if IsCode(err, CodeNotFound) {
				notFound.Add(1)
				return
			}
			t.Errorf("burn: %v", err)
		}()
	}
	wg.Wait()
	if wins.Load() != 1 || notFound.Load() != 49 {
		t.Fatalf("wins=%d notFound=%d", wins.Load(), notFound.Load())
	}
	if _, err := os.Stat(filepath.Join(dir, "att", p.ID)); !os.IsNotExist(err) {
		t.Fatal("blob dir survived")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(entries) != 0 {
		t.Fatalf("tmp has %d entries", len(entries))
	}
	// Non-burn pastes cannot be burned.
	k, _ := s.CreatePaste(ctx, newPaste(false, 0), nil)
	if _, err := s.Burn(ctx, k.ID); !IsCode(err, CodeInvalid) {
		t.Fatalf("burn of keep paste: %v", err)
	}
}

func TestSweepBatches(t *testing.T) {
	s, c, dir := open(t)
	ctx := context.Background()
	var ids []string
	for i := 0; i < 230; i++ {
		var blobs []NewBlob
		if i%50 == 0 {
			blobs = []NewBlob{blob(t, s, 10)}
		}
		p, err := s.CreatePaste(ctx, newPaste(false, 300), blobs)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	keep, _ := s.CreatePaste(ctx, newPaste(false, 3600), nil)
	if got, err := s.SweepExpired(ctx); err != nil || len(got) != 0 {
		t.Fatalf("premature sweep: %v %v", got, err)
	}
	c.Add(300 * time.Second)
	got, err := s.SweepExpired(ctx)
	if err != nil || len(got) != 230 {
		t.Fatalf("swept %d: %v", len(got), err)
	}
	if _, err := s.GetPaste(ctx, keep.ID); err != nil {
		t.Fatal("unexpired paste swept")
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "att")); len(entries) != 0 {
		t.Fatalf("att has %d entries", len(entries))
	}
	var n int
	s.r.QueryRow("SELECT COUNT(*) FROM paste_versions").Scan(&n)
	if n != 1 {
		t.Fatalf("cascade left %d versions", n)
	}
}

func TestOrphansAndTemp(t *testing.T) {
	s, _, dir := open(t)
	ctx := context.Background()
	p, err := s.CreatePaste(ctx, newPaste(false, 0), []NewBlob{blob(t, s, 10)})
	if err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(dir, "att", "ZZZZZZZZZZ")
	os.MkdirAll(orphan, 0o700)
	os.WriteFile(filepath.Join(orphan, "x"), []byte("x"), 0o600)
	n, err := s.RemoveOrphanBlobs(ctx)
	if err != nil || n != 1 {
		t.Fatalf("removed %d: %v", n, err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan survived")
	}
	if _, err := os.Stat(filepath.Join(dir, "att", p.ID)); err != nil {
		t.Fatal("live blobs removed")
	}

	f, _ := s.TempFile()
	f.Close()
	if err := s.CleanTemp(time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.Name()); err != nil {
		t.Fatal("fresh temp file removed")
	}
	s.CleanTemp(0)
	if _, err := os.Stat(f.Name()); !os.IsNotExist(err) {
		t.Fatal("old temp file kept")
	}
}

func TestCommentRules(t *testing.T) {
	s, _, _ := open(t)
	ctx := context.Background()
	p, _ := s.CreatePaste(ctx, newPaste(false, 0), nil)
	id := func() string { return string(NewPasteID() + NewPasteID() + "ab") }
	allow := func(*Comment) error { return nil }
	mk := func(parent *string) *Comment {
		c, err := s.CreateComment(ctx, p.ID, NewComment{ID: id(), ParentID: parent, Author: "guest",
			Verifier: rnd(32), Tag: "0123456789abcdef", Content: rnd(40)}, false, 100)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	top := mk(nil)
	r1 := mk(&top.ID)
	if _, err := s.CreateComment(ctx, p.ID, NewComment{ID: id(), ParentID: &r1.ID, Author: "guest",
		Verifier: rnd(32), Tag: "x", Content: rnd(40)}, false, 100); !IsCode(err, CodeInvalid) {
		t.Fatalf("reply to reply: %v", err)
	}
	if _, err := s.UpdateComment(ctx, p.ID, top.ID, rnd(40), 1, 10, nil, func(*Comment) error { return ErrForbidden }); !IsCode(err, CodeForbidden) {
		t.Fatalf("authorize not honoured: %v", err)
	}
	res, err := s.DeleteComment(ctx, p.ID, top.ID, allow)
	if err != nil || res.Tombstoned == nil || len(res.Removed) != 0 {
		t.Fatalf("tombstone: %+v %v", res, err)
	}
	var versions int
	s.r.QueryRow("SELECT COUNT(*) FROM comment_versions WHERE comment_id = ?", top.ID).Scan(&versions)
	if versions != 0 {
		t.Fatal("tombstone kept versions")
	}
	res, err = s.DeleteComment(ctx, p.ID, r1.ID, allow)
	if err != nil || len(res.Removed) != 2 || res.Removed[0] != r1.ID || res.Removed[1] != top.ID {
		t.Fatalf("cascade: %+v %v", res, err)
	}
	var left int
	s.r.QueryRow("SELECT COUNT(*) FROM comments").Scan(&left)
	if left != 0 {
		t.Fatalf("%d comments left", left)
	}
}

func TestImageRefs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{MaxImageRefs: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	r1, r2, r3, r4 := rnd(32), rnd(32), rnd(32), rnd(32)
	np := newPaste(false, 0)
	np.ImageRefs = [][]byte{r1, r1}
	p, err := s.CreatePaste(ctx, np, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.HasImageRef(ctx, p.ID, r1); !ok {
		t.Fatal("ref not stored")
	}
	if _, err := s.UpdatePaste(ctx, p.ID, rnd(40), 1, 10, [][]byte{r2, r3, r4}); !IsCode(err, CodeTooLarge) {
		t.Fatalf("over cap: %v", err)
	}
	if n, _ := s.ImageRefCount(ctx, p.ID); n != 1 {
		t.Fatalf("partial write: %d refs", n)
	}
	if got, _ := s.GetPaste(ctx, p.ID); got.Version != 1 {
		t.Fatal("rejected update changed the paste")
	}
	if err := s.AddImageRefs(ctx, p.ID, [][]byte{r1, r2, r3}, func(*Paste) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.AddImageRefs(ctx, p.ID, [][]byte{r4}, func(*Paste) error { return ErrLocked("x") }); !IsCode(err, CodeLocked) {
		t.Fatalf("authorize ignored: %v", err)
	}
	if err := s.AddImageRefs(ctx, p.ID, [][]byte{{1, 2}}, func(*Paste) error { return nil }); !IsCode(err, CodeInvalid) {
		t.Fatalf("short ref accepted: %v", err)
	}
	// Burn snapshot, then cascade on delete.
	bp := newPaste(true, 0)
	bp.ImageRefs = [][]byte{r4}
	b, _ := s.CreatePaste(ctx, bp, nil)
	burned, err := s.Burn(ctx, b.ID)
	if err != nil || len(burned.ImageRefs) != 1 {
		t.Fatalf("burn refs %v %v", burned, err)
	}
	if err := s.DeletePaste(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	s.r.QueryRow("SELECT COUNT(*) FROM image_refs").Scan(&left)
	if left != 0 {
		t.Fatalf("%d refs survived their pastes", left)
	}
	s.Close()
}

func TestMigrateV1(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Turn it into a v1 database.
	if _, err := s.w.Exec("DROP TABLE image_refs; PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	s.r.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 2 {
		t.Fatalf("user_version %d", v)
	}
	if _, err := s.ImageRefCount(context.Background(), "AAAAAAAAAA"); err != nil {
		t.Fatalf("image_refs missing after migration: %v", err)
	}
}
