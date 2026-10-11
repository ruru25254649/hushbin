package api_test

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"hushbin/server/api"
	"hushbin/server/config"
	"hushbin/server/store"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type testEnv struct {
	t     *testing.T
	cfg   config.Config
	srv   *api.Server
	ts    *httptest.Server
	store *store.Store
	clock *fakeClock
	dir   string
}

var testSPA = fstest.MapFS{
	"index.html":                  {Data: []byte("<!doctype html><title>Hushbin</title>")},
	"assets/index-Ab12Cd34.js":    {Data: []byte("console.log('hi')")},
	"assets/index-Ef56Gh78.css":   {Data: []byte("body{}")},
	"assets/argon2-0a1b2c3d.wasm": {Data: []byte("\x00asm\x01\x00\x00\x00")},
	"sw.js":                       {Data: []byte("self.addEventListener('fetch',()=>{})")},
	"manifest.webmanifest":        {Data: []byte(`{"name":"Hushbin"}`)},
	"favicon.svg":                 {Data: []byte("<svg/>")},
}

// newEnv starts a server with rate limits disabled unless mod sets them.
func newEnv(t *testing.T, mod func(c *config.Config), opts ...func(o *api.Options)) *testEnv {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.RateCreate, cfg.RateComment, cfg.RateUpload, cfg.RateAuth, cfg.RateGeneral =
		config.Rate{}, config.Rate{}, config.Rate{}, config.Rate{}, config.Rate{}
	if mod != nil {
		mod(&cfg)
	}
	clock := &fakeClock{t: time.UnixMilli(1_760_000_000_000)}
	st, err := store.Open(cfg.DataDir, store.Options{Now: clock.Now, MaxImageRefs: cfg.MaxImageRefs})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	o := api.Options{Config: cfg, Store: st, Logger: logger, Now: clock.Now, PingInterval: time.Hour}
	for _, f := range opts {
		f(&o)
	}
	srv := api.New(o, testSPA)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { st.Close() })
	t.Cleanup(ts.Close)
	t.Cleanup(srv.Shutdown)
	return &testEnv{t: t, cfg: cfg, srv: srv, ts: ts, store: st, clock: clock, dir: cfg.DataDir}
}

// ---------------------------------------------------------------------------
// Client-side helpers (the server never sees plaintext, so random bytes
// shaped like envelopes are all we need).

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("bad b64u %q: %v", s, err)
	}
	return b
}

func sha(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func tagOf(raw []byte) string {
	s := sha256.Sum256(append([]byte("tag:"), raw...))
	return hex.EncodeToString(s[:])[:16]
}

// envelope returns a fake v1 envelope with n ciphertext bytes (30+n total).
func envelope(n int) []byte {
	b := randBytes(30 + n)
	b[0], b[1] = 0x01, 0x00
	return b
}

func newID() string { return b64(randBytes(16)) }

type keys struct {
	read, owner []byte
}

func (k keys) readTok() string  { return b64(k.read) }
func (k keys) ownerTok() string { return b64(k.owner) }

type att struct {
	id   string
	meta []byte
	data []byte
}

func newAtt(n int) att { return att{id: newID(), meta: envelope(40), data: envelope(n)} }

type createReq struct {
	meta  map[string]any
	atts  []att
	extra func(mw *multipart.Writer) // extra parts
}

// defaultMeta returns a valid create meta for k.
func defaultMeta(k keys) map[string]any {
	return map[string]any{
		"burn": false, "discussion": true, "expiresIn": 604800,
		"salt":          b64(randBytes(16)),
		"kdf":           map[string]any{"alg": "argon2id", "m": 65536, "t": 3, "p": 1},
		"readVerifier":  b64(sha(k.read)),
		"ownerVerifier": b64(sha(k.owner)),
		"content":       b64(envelope(100)),
		"attachments":   []any{},
	}
}

func multipartBody(t *testing.T, parts func(mw *multipart.Writer)) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	parts(mw)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func jsonPart(t *testing.T, mw *multipart.Writer, name string, v any) {
	t.Helper()
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="`+name+`"; filename="blob"`)
	h.Set("Content-Type", "application/json")
	w, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if raw, ok := v.([]byte); ok {
		w.Write(raw)
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatal(err)
	}
}

func binPart(t *testing.T, mw *multipart.Writer, name string, data []byte) {
	t.Helper()
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="`+name+`"; filename="`+name+`"`)
	h.Set("Content-Type", "application/octet-stream")
	w, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(data)
}

func (e *testEnv) createRaw(req createReq, hdr map[string]string) *http.Response {
	e.t.Helper()
	if req.atts != nil {
		list := []any{}
		for _, a := range req.atts {
			list = append(list, map[string]any{"id": a.id, "meta": b64(a.meta)})
		}
		req.meta["attachments"] = list
	}
	body, ct := multipartBody(e.t, func(mw *multipart.Writer) {
		jsonPart(e.t, mw, "meta", req.meta)
		for _, a := range req.atts {
			binPart(e.t, mw, "att-"+a.id, a.data)
		}
		if req.extra != nil {
			req.extra(mw)
		}
	})
	if hdr == nil {
		hdr = map[string]string{}
	}
	hdr["Content-Type"] = ct
	return e.do("POST", "/api/pastes", hdr, body)
}

type created struct {
	ID        string `json:"id"`
	ExpiresAt *int64 `json:"expiresAt"`
	keys      keys
}

// create makes a paste; mod may adjust the meta.
func (e *testEnv) create(mod func(m map[string]any), atts ...att) created {
	e.t.Helper()
	k := keys{read: randBytes(32), owner: randBytes(32)}
	m := defaultMeta(k)
	if mod != nil {
		mod(m)
	}
	resp := e.createRaw(createReq{meta: m, atts: atts}, nil)
	var c created
	e.expectJSON(resp, http.StatusCreated, &c)
	c.keys = k
	return c
}

func (e *testEnv) do(method, path string, hdr map[string]string, body io.Reader) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func (e *testEnv) doJSON(method, path string, hdr map[string]string, v any) *http.Response {
	e.t.Helper()
	var body io.Reader
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			e.t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	if hdr == nil {
		hdr = map[string]string{}
	}
	hdr["Content-Type"] = "application/json"
	return e.do(method, path, hdr, body)
}

// expectJSON asserts the status and decodes the body into out (if non-nil).
func (e *testEnv) expectJSON(resp *http.Response, status int, out any) {
	e.t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		e.t.Fatalf("%s %s: status %d, want %d; body: %s", resp.Request.Method, resp.Request.URL.Path,
			resp.StatusCode, status, truncate(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			e.t.Fatalf("decode %s: %v", truncate(body), err)
		}
	}
}

type apiErr struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Version *int   `json:"version"`
}

// expectErr asserts an error response with the given status and code.
func (e *testEnv) expectErr(resp *http.Response, status int, code string) apiErr {
	e.t.Helper()
	var ae apiErr
	e.expectJSON(resp, status, &ae)
	if ae.Error != code {
		e.t.Fatalf("%s %s: error code %q, want %q (%s)", resp.Request.Method, resp.Request.URL.Path, ae.Error, code, ae.Message)
	}
	if ae.Message == "" {
		e.t.Fatalf("error without message")
	}
	return ae
}

func truncate(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "..."
	}
	return string(b)
}

func readHdr(k keys) map[string]string { return map[string]string{"X-Read-Auth": k.readTok()} }

func ownerHdr(k keys) map[string]string { return map[string]string{"X-Owner-Token": k.ownerTok()} }

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

type commentT struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parentId"`
	CreatedAt int64   `json:"createdAt"`
	UpdatedAt int64   `json:"updatedAt"`
	Author    string  `json:"author"`
	Tag       string  `json:"tag"`
	Deleted   bool    `json:"deleted"`
	Version   int     `json:"version"`
	Content   *string `json:"content"`
}

type attachmentT struct {
	ID        string  `json:"id"`
	CommentID *string `json:"commentId"`
	Size      int64   `json:"size"`
	Meta      string  `json:"meta"`
	Data      *string `json:"data"`
}

type openT struct {
	ID          string        `json:"id"`
	Burn        bool          `json:"burn"`
	Discussion  bool          `json:"discussion"`
	Locked      bool          `json:"locked"`
	CreatedAt   int64         `json:"createdAt"`
	UpdatedAt   int64         `json:"updatedAt"`
	ExpiresAt   *int64        `json:"expiresAt"`
	Version     int           `json:"version"`
	Content     string        `json:"content"`
	Attachments []attachmentT `json:"attachments"`
	Comments    []commentT    `json:"comments"`
}

func (e *testEnv) open(c created) openT {
	e.t.Helper()
	var o openT
	e.expectJSON(e.do("POST", "/api/pastes/"+c.ID+"/open", readHdr(c.keys), nil), http.StatusOK, &o)
	return o
}

// comment posts a comment; token is the author token (guest) unless owner.
func (e *testEnv) comment(c created, token []byte, owner bool, parent *string) (commentT, []byte) {
	e.t.Helper()
	hdr := readHdr(c.keys)
	if owner {
		hdr["X-Owner-Token"] = c.keys.ownerTok()
	} else {
		hdr["X-Author-Token"] = b64(token)
	}
	content := envelope(50)
	var ct commentT
	e.expectJSON(e.doJSON("POST", "/api/pastes/"+c.ID+"/comments", hdr,
		map[string]any{"id": newID(), "parentId": parent, "content": b64(content)}), http.StatusCreated, &ct)
	return ct, content
}

func (e *testEnv) upload(c created, hdr map[string]string, id string, commentID *string, data []byte) *http.Response {
	e.t.Helper()
	body, ct := multipartBody(e.t, func(mw *multipart.Writer) {
		jsonPart(e.t, mw, "meta", map[string]any{"id": id, "commentId": commentID, "meta": b64(envelope(20))})
		binPart(e.t, mw, "data", data)
	})
	return e.do("POST", "/api/pastes/"+c.ID+"/attachments", merge(hdr, map[string]string{"Content-Type": ct}), body)
}

func (e *testEnv) blobPath(pasteID, attID string) string {
	return filepath.Join(e.dir, "att", pasteID, attID)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ---------------------------------------------------------------------------
// SSE client

type sseClient struct {
	t      *testing.T
	resp   *http.Response
	events chan string // data payloads; ": ping" comments as "PING"
	done   chan struct{}
}

func (e *testEnv) subscribe(c created) *sseClient {
	e.t.Helper()
	resp := e.do("GET", "/api/pastes/"+c.ID+"/events", readHdr(c.keys), nil)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		e.t.Fatalf("events: status %d: %s", resp.StatusCode, b)
	}
	s := &sseClient{t: e.t, resp: resp, events: make(chan string, 100), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		defer close(s.events)
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "data: "):
				s.events <- strings.TrimPrefix(line, "data: ")
			case line == ": ping":
				s.events <- "PING"
			}
		}
	}()
	e.t.Cleanup(func() { resp.Body.Close() })
	return s
}

// next returns the next data event (skipping pings), decoded.
func (s *sseClient) next() map[string]any {
	s.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				s.t.Fatal("event stream closed")
			}
			if ev == "PING" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(ev), &m); err != nil {
				s.t.Fatalf("bad event %q: %v", ev, err)
			}
			return m
		case <-timeout:
			s.t.Fatal("timed out waiting for event")
		}
	}
}

// expectClosed waits for the server to end the stream.
func (s *sseClient) expectClosed() {
	s.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-s.events:
			if !ok {
				return
			}
			if ev != "PING" {
				s.t.Fatalf("unexpected event before close: %s", ev)
			}
		case <-timeout:
			s.t.Fatal("stream not closed")
		}
	}
}
