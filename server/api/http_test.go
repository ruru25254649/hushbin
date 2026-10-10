package api_test

import (
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"hushbin/server/config"
)

func TestRateLimitCreate(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.RateCreate = config.Rate{N: 2, Per: time.Hour} })
	e.create(nil)
	e.create(nil)
	k := keys{read: randBytes(32), owner: randBytes(32)}
	resp := e.createRaw(createReq{meta: defaultMeta(k)}, nil)
	ra, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || ra < 1 || ra > 1800 {
		t.Fatalf("Retry-After %q", resp.Header.Get("Retry-After"))
	}
	e.expectErr(resp, 429, "rate_limited")
	// Other endpoints are unaffected.
	e.expectJSON(e.do("GET", "/api/config", nil, nil), 200, nil)
	// Tokens refill over time.
	e.clock.Advance(31 * time.Minute)
	e.create(nil)
}

func TestRateLimitFailedAuth(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.RateAuth = config.Rate{N: 3, Per: time.Hour} })
	c := e.create(nil)
	// Successful auth is free.
	for i := 0; i < 5; i++ {
		e.open(c)
	}
	wrong := map[string]string{"X-Read-Auth": b64(randBytes(32))}
	for i := 0; i < 3; i++ {
		e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", wrong, nil), 403, "unauthorized")
	}
	// Budget exhausted: even the right password is refused for now, so
	// guessing cannot continue.
	e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/open", readHdr(c.keys), nil), 429, "rate_limited")
	// Owner-token guesses share the budget.
	e.expectErr(e.do("DELETE", "/api/pastes/"+c.ID, ownerHdr(c.keys), nil), 429, "rate_limited")
	// The public header needs no auth.
	e.expectJSON(e.do("GET", "/api/pastes/"+c.ID, nil, nil), 200, nil)
	e.clock.Advance(20 * time.Minute)
	e.open(c)

	// Wrong owner tokens count too.
	e2 := newEnv(t, func(c *config.Config) { c.RateAuth = config.Rate{N: 2, Per: time.Hour} })
	c2 := e2.create(nil)
	bad := map[string]string{"X-Owner-Token": b64(randBytes(32))}
	e2.expectErr(e2.doJSON("POST", "/api/pastes/"+c2.ID+"/lock", bad, map[string]any{"locked": true}), 403, "forbidden")
	e2.expectErr(e2.doJSON("POST", "/api/pastes/"+c2.ID+"/lock", bad, map[string]any{"locked": true}), 403, "forbidden")
	e2.expectErr(e2.doJSON("POST", "/api/pastes/"+c2.ID+"/lock", ownerHdr(c2.keys), map[string]any{"locked": true}), 429, "rate_limited")
}

func TestRateLimitClasses(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.RateComment = config.Rate{N: 2, Per: time.Hour}
		c.RateUpload = config.Rate{N: 1, Per: time.Hour}
	})
	c := e.create(nil)
	guest := randBytes(32)
	gc, _ := e.comment(c, guest, false, nil)
	// Edits share the comment budget.
	e.expectJSON(e.doJSON("PUT", "/api/pastes/"+c.ID+"/comments/"+gc.ID,
		merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(guest)}),
		map[string]any{"content": b64(envelope(5)), "baseVersion": 1}), 200, nil)
	e.expectErr(e.doJSON("POST", "/api/pastes/"+c.ID+"/comments",
		merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(guest)}),
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5))}), 429, "rate_limited")

	e.expectJSON(e.upload(c, merge(readHdr(c.keys), ownerHdr(c.keys)), newID(), nil, envelope(5)), 201, nil)
	e.expectErr(e.upload(c, merge(readHdr(c.keys), ownerHdr(c.keys)), newID(), nil, envelope(5)), 429, "rate_limited")

	g := newEnv(t, func(c *config.Config) { c.RateGeneral = config.Rate{N: 3, Per: time.Minute} })
	for i := 0; i < 3; i++ {
		g.expectJSON(g.do("GET", "/api/config", nil, nil), 200, nil)
	}
	g.expectErr(g.do("GET", "/api/config", nil, nil), 429, "rate_limited")
	// The general API limit does not apply to the SPA or health checks.
	for i := 0; i < 5; i++ {
		resp := g.do("GET", "/", nil, nil)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("static: %d", resp.StatusCode)
		}
	}
}

func TestTrustedProxyRateLimitKeys(t *testing.T) {
	limited := func(c *config.Config) { c.RateCreate = config.Rate{N: 1, Per: time.Hour} }
	k := keys{read: randBytes(32), owner: randBytes(32)}
	createAs := func(e *testEnv, xff string) *http.Response {
		hdr := map[string]string{}
		if xff != "" {
			hdr["X-Forwarded-For"] = xff
		}
		return e.createRaw(createReq{meta: defaultMeta(k)}, hdr)
	}

	// Untrusted peer: X-Forwarded-For is ignored, so spoofing does not help.
	e := newEnv(t, limited)
	e.expectJSON(createAs(e, "203.0.113.1"), 201, nil)
	e.expectErr(createAs(e, "203.0.113.2"), 429, "rate_limited")

	// Trusted peer (the test client connects from 127.0.0.1): the rightmost
	// untrusted address is the client.
	e = newEnv(t, func(c *config.Config) {
		limited(c)
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8")}
	})
	e.expectJSON(createAs(e, "203.0.113.1"), 201, nil)
	e.expectJSON(createAs(e, "203.0.113.2"), 201, nil)
	e.expectErr(createAs(e, "203.0.113.1"), 429, "rate_limited")
	// A client-supplied left entry cannot dodge the limit.
	e.expectErr(createAs(e, "198.51.100.7, 203.0.113.2"), 429, "rate_limited")
	// Trusted hops are skipped.
	e.expectErr(createAs(e, "203.0.113.2, 10.1.2.3"), 429, "rate_limited")
	e.expectJSON(createAs(e, "203.0.113.3, 10.1.2.3"), 201, nil)
}

func TestStaticSPA(t *testing.T) {
	e := newEnv(t, nil)
	get := func(path string) (*http.Response, string) {
		resp := e.do("GET", path, nil, nil)
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	for _, p := range []string{"/", "/p/AbCdEfGhIj", "/p/AbCdEfGhIj/", "/settings", "/index.html"} {
		resp, body := get(p)
		if resp.StatusCode != 200 || !strings.Contains(body, "<title>Hushbin") ||
			resp.Header.Get("Cache-Control") != "no-cache" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %q %q", p, resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("Content-Type"))
		}
	}
	cases := []struct{ path, ct, cache string }{
		{"/assets/index-Ab12Cd34.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/assets/index-Ef56Gh78.css", "text/css; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/assets/argon2-0a1b2c3d.wasm", "application/wasm", "public, max-age=31536000, immutable"},
		{"/sw.js", "text/javascript; charset=utf-8", "no-cache"},
		{"/manifest.webmanifest", "application/manifest+json", "no-cache"},
		{"/favicon.svg", "image/svg+xml", "no-cache"},
	}
	for _, tc := range cases {
		resp, _ := get(tc.path)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != tc.ct || resp.Header.Get("Cache-Control") != tc.cache {
			t.Fatalf("%s: %d ct=%q cache=%q", tc.path, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
		}
	}
	// Missing hashed assets are real 404s, not index.html.
	if resp, _ := get("/assets/missing-123.js"); resp.StatusCode != 404 {
		t.Fatalf("missing asset: %d", resp.StatusCode)
	}
	// ETag revalidation.
	resp, _ := get("/sw.js")
	tag := resp.Header.Get("ETag")
	if tag == "" {
		t.Fatal("no ETag")
	}
	r2 := e.do("GET", "/sw.js", map[string]string{"If-None-Match": tag}, nil)
	r2.Body.Close()
	if r2.StatusCode != 304 {
		t.Fatalf("revalidation: %d", r2.StatusCode)
	}
	// Unknown API paths are JSON 404s, not the SPA.
	e.expectErr(e.do("GET", "/api/nope", nil, nil), 404, "not_found")
	e.expectErr(e.do("PATCH", "/api/pastes/AbCdEfGhIj", nil, nil), 404, "not_found")
	// Non-GET on SPA paths.
	r3 := e.do("DELETE", "/", nil, nil)
	r3.Body.Close()
	if r3.StatusCode != 405 {
		t.Fatalf("DELETE /: %d", r3.StatusCode)
	}
}

func TestShareHealthAndSecurityHeaders(t *testing.T) {
	e := newEnv(t, nil)
	client := *e.ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest("POST", e.ts.URL+"/share", strings.NewReader("title=x&text=y"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Fatalf("share: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	h := e.do("GET", "/healthz", nil, nil)
	b, _ := io.ReadAll(h.Body)
	h.Body.Close()
	if h.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("healthz: %d %q", h.StatusCode, b)
	}

	want := map[string]string{
		"Content-Security-Policy":    "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; manifest-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'",
		"Referrer-Policy":            "no-referrer",
		"X-Content-Type-Options":     "nosniff",
		"Cross-Origin-Opener-Policy": "same-origin",
		"Permissions-Policy":         "camera=(), microphone=(), geolocation=()",
	}
	c := e.create(nil)
	for _, r := range []*http.Response{
		resp, h,
		e.do("GET", "/", nil, nil),
		e.do("GET", "/assets/index-Ab12Cd34.js", nil, nil),
		e.do("GET", "/api/config", nil, nil),
		e.do("POST", "/api/pastes/"+c.ID+"/open", nil, nil),
		e.do("GET", "/api/nope", nil, nil),
	} {
		r.Body.Close()
		for k, v := range want {
			if got := r.Header.Get(k); got != v {
				t.Fatalf("%s %s: header %s = %q", r.Request.Method, r.Request.URL.Path, k, got)
			}
		}
	}
	api := e.do("GET", "/api/config", nil, nil)
	api.Body.Close()
	if api.Header.Get("Cache-Control") != "no-store" || api.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("api headers %v", api.Header)
	}
}
