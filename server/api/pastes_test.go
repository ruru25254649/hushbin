package api_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"hushbin/server/config"
)

func TestConfigEndpoint(t *testing.T) {
	e := newEnv(t, nil)
	var c struct {
		Version             string  `json:"version"`
		MaxBody             int64   `json:"maxBody"`
		MaxComment          int64   `json:"maxComment"`
		MaxAttachment       int64   `json:"maxAttachment"`
		MaxPasteAttachments int64   `json:"maxPasteAttachments"`
		Expiries            []int64 `json:"expiries"`
	}
	e.expectJSON(e.do("GET", "/api/config", nil, nil), 200, &c)
	if c.Version != "0.1.0" || c.MaxBody != 2097152 || c.MaxComment != 262144 ||
		c.MaxAttachment != 26214400 || c.MaxPasteAttachments != 104857600 {
		t.Fatalf("unexpected config %+v", c)
	}
	want := []int64{300, 3600, 86400, 604800, 2592000, 0}
	if len(c.Expiries) != len(want) {
		t.Fatalf("expiries %v", c.Expiries)
	}
	for i := range want {
		if c.Expiries[i] != want[i] {
			t.Fatalf("expiries %v", c.Expiries)
		}
	}

	e2 := newEnv(t, func(c *config.Config) { c.AllowNever = false })
	e2.expectJSON(e2.do("GET", "/api/config", nil, nil), 200, &c)
	for _, v := range c.Expiries {
		if v == 0 {
			t.Fatalf("0 must be omitted when never is disallowed: %v", c.Expiries)
		}
	}
	e2.expectErr(e2.createRaw(createReq{meta: func() map[string]any {
		m := defaultMeta(keys{read: randBytes(32), owner: randBytes(32)})
		m["expiresIn"] = 0
		return m
	}()}, nil), 400, "invalid")
}

func TestCreateAndHeader(t *testing.T) {
	e := newEnv(t, nil)
	now := e.clock.Now().UnixMilli()
	var salt string
	c := e.create(func(m map[string]any) { salt = m["salt"].(string) })
	if len(c.ID) != 10 || strings.Trim(c.ID, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("bad paste id %q", c.ID)
	}
	if c.ExpiresAt == nil || *c.ExpiresAt != now+604800*1000 {
		t.Fatalf("expiresAt %v, want %d", c.ExpiresAt, now+604800000)
	}

	var h map[string]any
	e.expectJSON(e.do("GET", "/api/pastes/"+c.ID, nil, nil), 200, &h)
	if h["id"] != c.ID || h["burn"] != false || h["discussion"] != true || h["salt"] != salt ||
		h["createdAt"].(float64) != float64(now) || h["expiresAt"].(float64) != float64(now+604800000) {
		t.Fatalf("header %v", h)
	}
	kdf := h["kdf"].(map[string]any)
	if kdf["alg"] != "argon2id" || kdf["m"].(float64) != 65536 || kdf["t"].(float64) != 3 || kdf["p"].(float64) != 1 || len(kdf) != 4 {
		t.Fatalf("kdf %v", kdf)
	}
	if len(h) != 7 {
		t.Fatalf("header must not leak other fields: %v", h)
	}

	// No password, never expires.
	c2 := e.create(func(m map[string]any) { m["kdf"] = nil; m["expiresIn"] = 0 })
	if c2.ExpiresAt != nil {
		t.Fatalf("expiresAt should be null")
	}
	e.expectJSON(e.do("GET", "/api/pastes/"+c2.ID, nil, nil), 200, &h)
	if h["kdf"] != nil || h["expiresAt"] != nil {
		t.Fatalf("header %v", h)
	}

	// Unknown and malformed IDs.
	e.expectErr(e.do("GET", "/api/pastes/AAAAAAAAAA", nil, nil), 404, "not_found")
	e.expectErr(e.do("GET", "/api/pastes/short", nil, nil), 404, "not_found")
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.MaxBody = 1000
		c.MaxAttachment = 2000
		c.MaxPasteAttachments = 3000
		c.MaxAttachmentCount = 3
	})
	k := keys{read: randBytes(32), owner: randBytes(32)}
	cases := []struct {
		name   string
		mod    func(m map[string]any)
		status int
		code   string
	}{
		{"burn with discussion", func(m map[string]any) { m["burn"] = true; m["discussion"] = true }, 400, "invalid"},
		{"expiry not allowed", func(m map[string]any) { m["expiresIn"] = 1234 }, 400, "invalid"},
		{"expiry missing", func(m map[string]any) { delete(m, "expiresIn") }, 400, "invalid"},
		{"expiry wrong type", func(m map[string]any) { m["expiresIn"] = "300" }, 400, "invalid"},
		{"burn missing", func(m map[string]any) { delete(m, "burn") }, 400, "invalid"},
		{"salt short", func(m map[string]any) { m["salt"] = b64(randBytes(15)) }, 400, "invalid"},
		{"salt padded", func(m map[string]any) { m["salt"] = b64(randBytes(16)) + "==" }, 400, "invalid"},
		{"salt std alphabet", func(m map[string]any) { m["salt"] = "++++++++++++++++++++++" }, 400, "invalid"},
		{"kdf wrong alg", func(m map[string]any) {
			m["kdf"] = map[string]any{"alg": "scrypt", "m": 65536, "t": 3, "p": 1}
		}, 400, "invalid"},
		{"kdf extra key", func(m map[string]any) {
			m["kdf"] = map[string]any{"alg": "argon2id", "m": 65536, "t": 3, "p": 1, "x": 1}
		}, 400, "invalid"},
		{"kdf missing key", func(m map[string]any) { m["kdf"] = map[string]any{"alg": "argon2id", "m": 65536, "t": 3} }, 400, "invalid"},
		{"kdf bad type", func(m map[string]any) {
			m["kdf"] = map[string]any{"alg": "argon2id", "m": "65536", "t": 3, "p": 1}
		}, 400, "invalid"},
		{"kdf not object", func(m map[string]any) { m["kdf"] = "argon2id" }, 400, "invalid"},
		{"kdf zero t", func(m map[string]any) { m["kdf"] = map[string]any{"alg": "argon2id", "m": 65536, "t": 0, "p": 1} }, 400, "invalid"},
		{"read verifier short", func(m map[string]any) { m["readVerifier"] = b64(randBytes(31)) }, 400, "invalid"},
		{"owner verifier missing", func(m map[string]any) { delete(m, "ownerVerifier") }, 400, "invalid"},
		{"content missing", func(m map[string]any) { delete(m, "content") }, 400, "invalid"},
		{"content not b64", func(m map[string]any) { m["content"] = "not base64!" }, 400, "invalid"},
		{"content too short", func(m map[string]any) { m["content"] = b64(envelope(0)[:29]) }, 400, "invalid"},
		{"content bad version", func(m map[string]any) {
			b := envelope(10)
			b[0] = 2
			m["content"] = b64(b)
		}, 400, "invalid"},
		{"content too large", func(m map[string]any) { m["content"] = b64(envelope(1000)) }, 413, "too_large"},
		{"attachment meta without part", func(m map[string]any) {
			m["attachments"] = []any{map[string]any{"id": newID(), "meta": b64(envelope(10))}}
		}, 400, "invalid"},
		{"attachment bad id", func(m map[string]any) {
			m["attachments"] = []any{map[string]any{"id": "short", "meta": b64(envelope(10))}}
		}, 400, "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := defaultMeta(k)
			m["content"] = b64(envelope(10))
			tc.mod(m)
			e.expectErr(e.createRaw(createReq{meta: m}, nil), tc.status, tc.code)
		})
	}

	t.Run("max content ok", func(t *testing.T) {
		m := defaultMeta(k)
		m["content"] = b64(envelope(1000 - 30))
		e.expectJSON(e.createRaw(createReq{meta: m}, nil), 201, nil)
	})
	t.Run("not multipart", func(t *testing.T) {
		e.expectErr(e.doJSON("POST", "/api/pastes", nil, defaultMeta(k)), 400, "invalid")
	})
	t.Run("missing meta", func(t *testing.T) {
		body, ct := multipartBody(t, func(mw *multipart.Writer) { binPart(t, mw, "att-"+newID(), envelope(5)) })
		e.expectErr(e.do("POST", "/api/pastes", map[string]string{"Content-Type": ct}, body), 400, "invalid")
	})
	t.Run("malformed meta json", func(t *testing.T) {
		body, ct := multipartBody(t, func(mw *multipart.Writer) { jsonPart(t, mw, "meta", []byte("{nope")) })
		e.expectErr(e.do("POST", "/api/pastes", map[string]string{"Content-Type": ct}, body), 400, "invalid")
	})
	t.Run("unknown part", func(t *testing.T) {
		e.expectErr(e.createRaw(createReq{meta: defaultMeta(k), extra: func(mw *multipart.Writer) {
			binPart(t, mw, "evil", []byte("x"))
		}}, nil), 400, "invalid")
	})
	t.Run("attachment part without meta entry", func(t *testing.T) {
		e.expectErr(e.createRaw(createReq{meta: defaultMeta(k), atts: []att{}, extra: func(mw *multipart.Writer) {
			binPart(t, mw, "att-"+newID(), envelope(5))
		}}, nil), 400, "invalid")
	})
	t.Run("attachment data not an envelope", func(t *testing.T) {
		a := newAtt(10)
		a.data[0] = 9
		e.expectErr(e.createRaw(createReq{meta: defaultMeta(k), atts: []att{a}}, nil), 400, "invalid")
	})
	t.Run("duplicate attachment ids", func(t *testing.T) {
		a := newAtt(10)
		m := defaultMeta(k)
		m["attachments"] = []any{
			map[string]any{"id": a.id, "meta": b64(a.meta)},
			map[string]any{"id": a.id, "meta": b64(a.meta)},
		}
		body, ct := multipartBody(t, func(mw *multipart.Writer) {
			jsonPart(t, mw, "meta", m)
			binPart(t, mw, "att-"+a.id, a.data)
		})
		e.expectErr(e.do("POST", "/api/pastes", map[string]string{"Content-Type": ct}, body), 409, "conflict")
	})
	t.Run("attachment too large", func(t *testing.T) {
		e.expectErr(e.createRaw(createReq{meta: defaultMeta(k), atts: []att{newAtt(2000)}}, nil), 413, "too_large")
	})
	t.Run("attachments total too large", func(t *testing.T) {
		e.expectErr(e.createRaw(createReq{meta: defaultMeta(k), atts: []att{newAtt(1900), newAtt(1100)}}, nil), 413, "too_large")
	})
	t.Run("too many attachments", func(t *testing.T) {
		e.expectErr(e.createRaw(createReq{meta: defaultMeta(k), atts: []att{newAtt(1), newAtt(1), newAtt(1), newAtt(1)}}, nil), 413, "too_large")
	})
	t.Run("oversized body", func(t *testing.T) {
		big := bytes.Repeat([]byte("x"), 1<<20)
		body, ct := multipartBody(t, func(mw *multipart.Writer) {
			jsonPart(t, mw, "meta", defaultMeta(k))
			binPart(t, mw, "att-"+newID(), big)
		})
		e.expectErr(e.do("POST", "/api/pastes", map[string]string{"Content-Type": ct}, body), 413, "too_large")
	})

	// Nothing may leak into the blob or temp directories from rejected requests.
	for _, d := range []string{"att", "tmp"} {
		entries, _ := os.ReadDir(filepath.Join(e.dir, d))
		if len(entries) != 0 {
			t.Fatalf("%s/ not empty after rejected creates: %d entries", d, len(entries))
		}
	}
}

func TestCreateWithAttachmentsAndOpen(t *testing.T) {
	e := newEnv(t, nil)
	a1, a2 := newAtt(1000), newAtt(5)
	var content string
	c := e.create(func(m map[string]any) { content = m["content"].(string) }, a1, a2)
	for _, a := range []att{a1, a2} {
		b, err := os.ReadFile(e.blobPath(c.ID, a.id))
		if err != nil || !bytes.Equal(b, a.data) {
			t.Fatalf("blob %s not stored correctly: %v", a.id, err)
		}
	}

	// Read auth: missing, malformed, wrong.
	e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", nil, nil), 403, "unauthorized")
	e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", map[string]string{"X-Read-Auth": "abc"}, nil), 403, "unauthorized")
	e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", map[string]string{"X-Read-Auth": b64(randBytes(32))}, nil), 403, "unauthorized")
	// The owner token is not a read token.
	e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", map[string]string{"X-Read-Auth": c.keys.ownerTok()}, nil), 403, "unauthorized")

	o := e.open(c)
	if o.ID != c.ID || o.Burn || !o.Discussion || o.Locked || o.Version != 1 || o.Content != content ||
		o.CreatedAt != o.UpdatedAt || o.ExpiresAt == nil || len(o.Comments) != 0 {
		t.Fatalf("open %+v", o)
	}
	if len(o.Attachments) != 2 {
		t.Fatalf("attachments %+v", o.Attachments)
	}
	for i, a := range []att{a1, a2} {
		got := o.Attachments[i]
		if got.ID != a.id || got.CommentID != nil || got.Size != int64(len(a.data)) || got.Meta != b64(a.meta) || got.Data != nil {
			t.Fatalf("attachment %d: %+v", i, got)
		}
	}
	// Opening a keep paste twice is fine.
	e.open(c)
}

func TestBurn(t *testing.T) {
	e := newEnv(t, nil)
	a := newAtt(3000)
	var content string
	c := e.create(func(m map[string]any) {
		m["burn"], m["discussion"], m["kdf"] = true, false, nil
		content = m["content"].(string)
	}, a)

	// The public header never burns.
	for i := 0; i < 3; i++ {
		var h map[string]any
		e.expectJSON(e.do("GET", "/api/pastes/"+c.ID, nil, nil), 200, &h)
		if h["burn"] != true || h["discussion"] != false {
			t.Fatalf("header %v", h)
		}
	}
	// A wrong password never burns.
	for i := 0; i < 3; i++ {
		e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", map[string]string{"X-Read-Auth": b64(randBytes(32))}, nil), 403, "unauthorized")
	}
	// Endpoints that would bypass burn-on-open are refused.
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/versions", readHdr(c.keys), nil), 400, "invalid")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/versions/1", readHdr(c.keys), nil), 400, "invalid")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/attachments/"+a.id, readHdr(c.keys), nil), 400, "invalid")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/events", readHdr(c.keys), nil), 400, "invalid")
	e.expectErr(e.upload(c, merge(readHdr(c.keys), ownerHdr(c.keys)), newID(), nil, envelope(5)), 400, "invalid")
	e.expectErr(e.doJSON("PUT", "/api/pastes/"+c.ID, ownerHdr(c.keys),
		map[string]any{"content": b64(envelope(5)), "baseVersion": 1}), 400, "invalid")
	e.expectErr(e.doJSON("POST", "/api/pastes/"+c.ID+"/comments", merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(randBytes(32))}),
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 403, "locked")
	// Answered before read auth: no password oracle that avoids burning.
	e.expectErr(e.doJSON("POST", "/api/pastes/"+c.ID+"/comments", map[string]string{"X-Read-Auth": b64(randBytes(32))},
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 403, "locked")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/versions", map[string]string{"X-Read-Auth": b64(randBytes(32))}, nil), 400, "invalid")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/comments/"+newID()+"/versions", map[string]string{"X-Read-Auth": b64(randBytes(32))}, nil), 404, "not_found")
	e.expectErr(e.do("DELETE", "/api/pastes/"+c.ID+"/comments/"+newID(), map[string]string{"X-Read-Auth": b64(randBytes(32))}, nil), 404, "not_found")

	o := e.open(c)
	if !o.Burn || o.Discussion || o.Content != content || len(o.Comments) != 0 || o.Comments == nil {
		t.Fatalf("burn open %+v", o)
	}
	if len(o.Attachments) != 1 || o.Attachments[0].Data == nil || o.Attachments[0].Size != int64(len(a.data)) {
		t.Fatalf("burn attachments %+v", o.Attachments)
	}
	if got := unb64(t, *o.Attachments[0].Data); !bytes.Equal(got, a.data) {
		t.Fatalf("inlined data mismatch")
	}
	if o.Attachments[0].Meta != b64(a.meta) {
		t.Fatalf("meta mismatch")
	}

	// Gone for good: rows and blobs.
	e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID, nil, nil), 404, "not_found")
	if exists(filepath.Join(e.dir, "att", c.ID)) {
		t.Fatalf("blob directory survived burn")
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "tmp")); len(entries) != 0 {
		t.Fatalf("tmp not cleaned after burn: %d entries", len(entries))
	}
}

func TestBurnConcurrentOpen(t *testing.T) {
	e := newEnv(t, nil)
	for round := 0; round < 3; round++ {
		c := e.create(func(m map[string]any) { m["burn"], m["discussion"] = true, false }, newAtt(100))
		const n = 24
		var wg sync.WaitGroup
		codes := make(chan int, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				req, _ := http.NewRequest("POST", e.ts.URL+"/api/pastes/"+c.ID+"/open", nil)
				req.Header.Set("X-Read-Auth", c.keys.readTok())
				resp, err := e.ts.Client().Do(req)
				if err != nil {
					codes <- -1
					return
				}
				var o openT
				if resp.StatusCode == 200 {
					e.expectJSON(resp, 200, &o)
					if len(o.Attachments) != 1 || o.Attachments[0].Data == nil {
						codes <- -2
						return
					}
				} else {
					resp.Body.Close()
				}
				codes <- resp.StatusCode
			}()
		}
		close(start)
		wg.Wait()
		close(codes)
		ok, notFound := 0, 0
		for code := range codes {
			switch code {
			case 200:
				ok++
			case 404:
				notFound++
			default:
				t.Fatalf("unexpected result %d", code)
			}
		}
		if ok != 1 || notFound != n-1 {
			t.Fatalf("round %d: %d successes, %d not found; want exactly one success", round, ok, notFound)
		}
	}
}

func TestUpdatePasteAndVersions(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.MaxVersions = 3; c.MaxBody = 500 })
	var v1 string
	c := e.create(func(m map[string]any) { m["content"] = b64(envelope(10)); v1 = m["content"].(string) })
	path := "/api/pastes/" + c.ID
	v2 := b64(envelope(20))

	// Owner auth.
	e.expectErr(e.doJSON("PUT", path, nil, map[string]any{"content": v2, "baseVersion": 1}), 403, "forbidden")
	e.expectErr(e.doJSON("PUT", path, map[string]string{"X-Owner-Token": b64(randBytes(32))}, map[string]any{"content": v2, "baseVersion": 1}), 403, "forbidden")
	e.expectErr(e.doJSON("PUT", path, readHdr(c.keys), map[string]any{"content": v2, "baseVersion": 1}), 403, "forbidden")
	// Validation.
	e.expectErr(e.doJSON("PUT", path, ownerHdr(c.keys), map[string]any{"content": v2}), 400, "invalid")
	e.expectErr(e.doJSON("PUT", path, ownerHdr(c.keys), map[string]any{"content": "!!", "baseVersion": 1}), 400, "invalid")
	e.expectErr(e.doJSON("PUT", path, ownerHdr(c.keys), map[string]any{"content": b64(envelope(500)), "baseVersion": 1}), 413, "too_large")
	e.expectErr(e.do("PUT", path, ownerHdr(c.keys), strings.NewReader(`{"content":`)), 400, "invalid")
	// Bodies beyond the JSON limit are cut off by MaxBytesReader.
	e.expectErr(e.doJSON("PUT", path, ownerHdr(c.keys), map[string]any{"content": strings.Repeat("A", 200<<10), "baseVersion": 1}), 413, "too_large")
	e.expectErr(e.do("PUT", path, ownerHdr(c.keys), strings.NewReader(`{"baseVersion":1,"pad":"`+strings.Repeat("A", 200<<10)+`"}`)), 413, "too_large")

	e.clock.Advance(1500 * time.Millisecond)
	var up struct {
		Version   int   `json:"version"`
		UpdatedAt int64 `json:"updatedAt"`
	}
	e.expectJSON(e.doJSON("PUT", path, ownerHdr(c.keys), map[string]any{"content": v2, "baseVersion": 1}), 200, &up)
	if up.Version != 2 || up.UpdatedAt != e.clock.Now().UnixMilli() {
		t.Fatalf("update %+v", up)
	}
	// Stale base version.
	ae := e.expectErr(e.doJSON("PUT", path, ownerHdr(c.keys), map[string]any{"content": b64(envelope(5)), "baseVersion": 1}), 409, "conflict")
	if ae.Version == nil || *ae.Version != 2 {
		t.Fatalf("conflict must report current version: %+v", ae)
	}
	o := e.open(c)
	if o.Version != 2 || o.Content != v2 || o.UpdatedAt != up.UpdatedAt || o.CreatedAt == o.UpdatedAt {
		t.Fatalf("open after update %+v", o)
	}

	// Versions (read auth).
	e.expectErr(e.do("GET", path+"/versions", nil, nil), 403, "unauthorized")
	var list []map[string]any
	e.expectJSON(e.do("GET", path+"/versions", readHdr(c.keys), nil), 200, &list)
	if len(list) != 2 || list[0]["version"].(float64) != 1 || list[1]["version"].(float64) != 2 ||
		list[1]["createdAt"].(float64) != float64(up.UpdatedAt) || list[0]["content"] != nil {
		t.Fatalf("versions %v", list)
	}
	var ver struct {
		Version   int    `json:"version"`
		CreatedAt int64  `json:"createdAt"`
		Content   string `json:"content"`
	}
	e.expectJSON(e.do("GET", path+"/versions/1", readHdr(c.keys), nil), 200, &ver)
	if ver.Version != 1 || ver.Content != v1 || ver.CreatedAt != o.CreatedAt {
		t.Fatalf("version 1 %+v", ver)
	}
	e.expectErr(e.do("GET", path+"/versions/3", readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("GET", path+"/versions/0", readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("GET", path+"/versions/abc", readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("GET", path+"/versions/01", readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("GET", path+"/versions/1", nil, nil), 403, "unauthorized")

	// Version cap (HB_MAX_VERSIONS=3).
	e.expectJSON(e.doJSON("PUT", path, ownerHdr(c.keys), map[string]any{"content": b64(envelope(5)), "baseVersion": 2}), 200, &up)
	e.expectErr(e.doJSON("PUT", path, ownerHdr(c.keys), map[string]any{"content": b64(envelope(5)), "baseVersion": 3}), 413, "too_large")
}

func TestDeletePaste(t *testing.T) {
	e := newEnv(t, nil)
	a := newAtt(100)
	c := e.create(nil, a)
	if !exists(e.blobPath(c.ID, a.id)) {
		t.Fatal("blob missing")
	}
	path := "/api/pastes/" + c.ID
	e.expectErr(e.do("DELETE", path, nil, nil), 403, "forbidden")
	e.expectErr(e.do("DELETE", path, map[string]string{"X-Owner-Token": c.keys.readTok()}, nil), 403, "forbidden")
	e.expectErr(e.do("DELETE", path, map[string]string{"X-Owner-Token": "garbage"}, nil), 403, "forbidden")

	resp := e.do("DELETE", path, ownerHdr(c.keys), nil)
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	e.expectErr(e.do("GET", path, nil, nil), 404, "not_found")
	e.expectErr(e.do("POST", path+"/open", readHdr(c.keys), nil), 404, "not_found")
	e.expectErr(e.do("DELETE", path, ownerHdr(c.keys), nil), 404, "not_found")
	if exists(filepath.Join(e.dir, "att", c.ID)) {
		t.Fatal("blob directory survived delete")
	}
}

func TestLock(t *testing.T) {
	e := newEnv(t, nil)
	c := e.create(nil)
	path := "/api/pastes/" + c.ID + "/lock"
	guest := randBytes(32)

	e.expectErr(e.doJSON("POST", path, nil, map[string]any{"locked": true}), 403, "forbidden")
	e.expectErr(e.doJSON("POST", path, readHdr(c.keys), map[string]any{"locked": true}), 403, "forbidden")
	e.expectErr(e.doJSON("POST", path, ownerHdr(c.keys), map[string]any{}), 400, "invalid")
	e.expectErr(e.doJSON("POST", path, ownerHdr(c.keys), map[string]any{"locked": "yes"}), 400, "invalid")

	gc, _ := e.comment(c, guest, false, nil)
	oc, _ := e.comment(c, nil, true, nil)

	var res map[string]any
	e.expectJSON(e.doJSON("POST", path, ownerHdr(c.keys), map[string]any{"locked": true}), 200, &res)
	if res["locked"] != true || len(res) != 1 {
		t.Fatalf("lock response %v", res)
	}
	if !e.open(c).Locked {
		t.Fatal("open should report locked")
	}

	commentPath := "/api/pastes/" + c.ID + "/comments"
	guestHdr := merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(guest)})
	// Guests can neither post, reply, edit nor attach while locked...
	e.expectErr(e.doJSON("POST", commentPath, guestHdr, map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 403, "locked")
	e.expectErr(e.doJSON("POST", commentPath, guestHdr, map[string]any{"id": newID(), "parentId": oc.ID, "content": b64(envelope(5))}), 403, "locked")
	e.expectErr(e.doJSON("PUT", commentPath+"/"+gc.ID, guestHdr, map[string]any{"content": b64(envelope(5)), "baseVersion": 1}), 403, "locked")
	e.expectErr(e.upload(c, readHdr(c.keys), newID(), &gc.ID, envelope(10)), 403, "locked")
	// ...but the owner can.
	e.comment(c, nil, true, nil)
	e.comment(c, nil, true, &gc.ID)
	e.expectJSON(e.doJSON("PUT", commentPath+"/"+oc.ID, merge(readHdr(c.keys), ownerHdr(c.keys)),
		map[string]any{"content": b64(envelope(5)), "baseVersion": 1}), 200, nil)
	e.expectJSON(e.upload(c, merge(readHdr(c.keys), ownerHdr(c.keys)), newID(), &oc.ID, envelope(10)), 201, nil)
	// Guests may still delete their own comment while locked (DESIGN.md
	// restricts only creating and editing).
	resp := e.do("DELETE", commentPath+"/"+gc.ID, guestHdr, nil)
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("guest delete while locked: %d", resp.StatusCode)
	}

	e.expectJSON(e.doJSON("POST", path, ownerHdr(c.keys), map[string]any{"locked": false}), 200, &res)
	if res["locked"] != false || e.open(c).Locked {
		t.Fatal("unlock failed")
	}
	e.comment(c, guest, false, nil)

	// Lock needs a discussion.
	nd := e.create(func(m map[string]any) { m["discussion"] = false })
	e.expectErr(e.doJSON("POST", "/api/pastes/"+nd.ID+"/lock", ownerHdr(nd.keys), map[string]any{"locked": true}), 400, "invalid")
}
