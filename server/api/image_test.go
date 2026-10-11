package api_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"hushbin/server/api"
	"hushbin/server/config"
	"hushbin/server/imageproxy"
)

var testPNG = func() []byte {
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	return buf.Bytes()
}()

func imgRef(u string) string {
	s := sha256.Sum256([]byte(u))
	return b64(s[:])
}

// imageUpstream is a local image host. It counts hits per path.
type imageUpstream struct {
	*httptest.Server
	mu         sync.Mutex
	hits       map[string]int
	flaky      int    // remaining failures for /flaky.png
	redirectTo string // target of /to-target
}

func newImageUpstream(t *testing.T) *imageUpstream {
	u := &imageUpstream{hits: map[string]int{}, flaky: 1}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits[r.URL.Path]++
		flaky := u.flaky
		if r.URL.Path == "/flaky.png" && u.flaky > 0 {
			u.flaky--
		}
		u.mu.Unlock()
		switch {
		case r.URL.Path == "/a.png" || r.URL.Path == "/b.png" || r.URL.Path == "/c.png" || r.URL.Path == "/d.png":
			w.Header().Set("Content-Type", "application/octet-stream") // ignored
			w.Write(testPNG)
		case r.URL.Path == "/html":
			w.Header().Set("Content-Type", "image/png") // lies
			w.Write([]byte("<!doctype html><html><script>alert(1)</script></html>"))
		case r.URL.Path == "/svg":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><rect/></svg>`))
		case r.URL.Path == "/tiny":
			w.Write([]byte{0x89, 'P', 'N'})
		case r.URL.Path == "/big.png":
			w.Write(append(append([]byte{}, testPNG...), make([]byte, 5000)...))
		case r.URL.Path == "/missing.png":
			http.NotFound(w, r)
		case r.URL.Path == "/flaky.png":
			if flaky > 0 {
				http.Error(w, "oops", http.StatusInternalServerError)
				return
			}
			w.Write(testPNG)
		case strings.HasPrefix(r.URL.Path, "/redir/"):
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/redir/"))
			if n == 0 {
				w.Write(testPNG)
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/redir/%d", n-1), http.StatusFound)
		case r.URL.Path == "/to-loopback":
			http.Redirect(w, r, "http://127.0.0.1/a.png", http.StatusFound)
		case r.URL.Path == "/to-metadata":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
		case r.URL.Path == "/to-ftp":
			http.Redirect(w, r, "ftp://example.com/a.png", http.StatusFound)
		case r.URL.Path == "/to-target":
			u.mu.Lock()
			target := u.redirectTo
			u.mu.Unlock()
			http.Redirect(w, r, target, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *imageUpstream) setRedirect(target string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.redirectTo = target
}

func (u *imageUpstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[path]
}

func allowPrivate(c *config.Config) { c.ImageProxyAllowPrivate = true }

func (e *testEnv) fetchImage(id string, hdr map[string]string, u string) *http.Response {
	e.t.Helper()
	return e.doJSON("POST", "/api/pastes/"+id+"/image", hdr, map[string]any{"url": u})
}

func expectImage(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("image: status %d: %s", resp.StatusCode, body)
	}
	h := resp.Header
	if h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "private, max-age=3600" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "default-src 'none'; sandbox" ||
		h.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("image headers %v", h)
	}
	return body
}

func withRefs(urls ...string) func(m map[string]any) {
	return func(m map[string]any) {
		refs := []string{}
		for _, u := range urls {
			refs = append(refs, imgRef(u))
		}
		m["imageRefs"] = refs
	}
}

func TestImageProxyHappyPath(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, allowPrivate)
	img := up.URL + "/a.png"
	c := e.create(withRefs(img))

	var cfg map[string]any
	e.expectJSON(e.do("GET", "/api/config", nil, nil), 200, &cfg)
	if cfg["imageProxy"] != true {
		t.Fatalf("config %v", cfg)
	}

	if got := expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), img)); !bytes.Equal(got, testPNG) {
		t.Fatal("image bytes differ")
	}
	// Cached: no second upstream hit.
	expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), img))
	if n := up.count("/a.png"); n != 1 {
		t.Fatalf("upstream hit %d times", n)
	}

	// Only registered URLs; the exact string is hashed.
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), up.URL+"/b.png"), 404, "not_found")
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), img+"?"), 404, "not_found")
	if up.count("/b.png") != 0 {
		t.Fatal("unregistered URL fetched")
	}
	// Auth.
	e.expectErr(e.fetchImage(c.ID, nil, img), 403, "unauthorized")
	e.expectErr(e.fetchImage(c.ID, map[string]string{"X-Read-Auth": b64(randBytes(32))}, img), 403, "unauthorized")
	e.expectErr(e.fetchImage(c.ID, ownerHdr(c.keys), img), 403, "unauthorized")
	e.expectErr(e.fetchImage("AAAAAAAAAA", readHdr(c.keys), img), 404, "not_found")
	// Request validation.
	e.expectErr(e.doJSON("POST", "/api/pastes/"+c.ID+"/image", readHdr(c.keys), map[string]any{}), 400, "invalid")
	for _, bad := range []string{"ftp://example.com/a.png", "https://u:p@example.com/a.png", "/a.png", "data:image/png;base64,AAAA"} {
		e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), bad), 400, "invalid")
	}
	e.expectErr(e.do("POST", "/api/pastes/"+c.ID+"/image", readHdr(c.keys),
		strings.NewReader(`{"url":"`+strings.Repeat("a", 10<<10)+`"}`)), 413, "too_large")
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID+"/image", readHdr(c.keys), nil), 404, "not_found")
}

func TestImageProxyRefusesPrivateByDefault(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, nil) // HB_IMAGE_PROXY_ALLOW_PRIVATE=false
	loop := "http://127.0.0.1/a.png"
	local := "http://localhost/a.png"
	metadata := "http://169.254.169.254/latest/meta-data"
	mapped := "http://[::ffff:127.0.0.1]/a.png"
	withPort := up.URL + "/a.png" // non-default port
	c := e.create(withRefs(loop, local, metadata, mapped, withPort))
	for _, u := range []string{loop, local, metadata, mapped} {
		e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), u), 502, "upstream")
	}
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), withPort), 400, "invalid")
	if up.count("/a.png") != 0 {
		t.Fatal("upstream reached")
	}
}

func TestImageProxyRedirects(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, allowPrivate)
	ok3, tooMany := up.URL+"/redir/3", up.URL+"/redir/4"
	toFTP := up.URL + "/to-ftp"
	c := e.create(withRefs(ok3, tooMany, toFTP))
	expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), ok3))
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), tooMany), 502, "upstream")
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), toFTP), 502, "upstream")

}

func addrPort(t *testing.T, rawURL string) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(strings.TrimPrefix(rawURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

// TestImageProxyAddressCheckAtDialTime runs the production address rules
// against a *reachable* loopback image host. Its port is made acceptable
// (test hook), so only the dial-time address check can stop the fetch.
func TestImageProxyAddressCheckAtDialTime(t *testing.T) {
	hop := newImageUpstream(t)    // first hop, exempt from all checks
	target := newImageUpstream(t) // loopback image host, port allowed
	targetAP := addrPort(t, target.URL)
	hop.setRedirect("http://127.0.0.1:" + strconv.Itoa(int(targetAP.Port())) + "/a.png")

	// Sanity: the target really serves images when private addresses are allowed.
	lax := imageproxy.New(imageproxy.Config{AllowPrivate: true})
	if _, err := lax.Get(t.Context(), target.URL+"/d.png"); err != nil || target.count("/d.png") != 1 {
		t.Fatalf("target not reachable: %v", err)
	}

	e := newEnv(t, nil, func(o *api.Options) {
		o.ImageProxy = imageproxy.New(imageproxy.Config{
			Exempt:     []netip.AddrPort{addrPort(t, hop.URL)},
			ExtraPorts: []uint16{targetAP.Port()},
			Now:        o.Now,
		})
	})
	port := strconv.Itoa(int(targetAP.Port()))
	direct := "http://127.0.0.1:" + port + "/a.png"
	viaName := "http://localhost:" + port + "/a.png"
	mapped := "http://[::ffff:127.0.0.1]:" + port + "/a.png"
	baseline := hop.URL + "/b.png"
	redirect := hop.URL + "/to-target"
	c := e.create(withRefs(direct, viaName, mapped, baseline, redirect))

	expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), baseline)) // exempt hop works
	for _, u := range []string{direct, viaName, mapped, redirect} {
		e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), u), 502, "upstream")
	}
	if target.count("/a.png") != 0 {
		t.Fatalf("loopback image host was reached %d times", target.count("/a.png"))
	}
	if hop.count("/to-target") != 1 {
		t.Fatal("redirect hop not exercised")
	}
}

func TestImageProxyContentChecks(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, func(c *config.Config) {
		allowPrivate(c)
		c.ImageProxyMaxBytes = 1000
	})
	paths := []string{"/html", "/svg", "/tiny", "/big.png", "/missing.png", "/flaky.png"}
	var urls []string
	for _, p := range paths {
		urls = append(urls, up.URL+p)
	}
	c := e.create(withRefs(urls...))
	get := func(p string) *http.Response { return e.fetchImage(c.ID, readHdr(c.keys), up.URL+p) }

	e.expectErr(get("/html"), 415, "not_image")
	e.expectErr(get("/svg"), 415, "not_image")
	e.expectErr(get("/tiny"), 415, "not_image")
	e.expectErr(get("/big.png"), 413, "too_large")
	e.expectErr(get("/missing.png"), 502, "upstream")

	// Failures are cached for 5 minutes.
	e.expectErr(get("/flaky.png"), 502, "upstream")
	e.expectErr(get("/flaky.png"), 502, "upstream")
	e.expectErr(get("/html"), 415, "not_image")
	if up.count("/flaky.png") != 1 || up.count("/html") != 1 {
		t.Fatalf("negative cache: flaky=%d html=%d", up.count("/flaky.png"), up.count("/html"))
	}
	e.clock.Advance(5*time.Minute + time.Second)
	expectImage(t, get("/flaky.png"))
	if up.count("/flaky.png") != 2 {
		t.Fatal("negative entry did not expire")
	}
}

func TestImageProxyHostAllowlist(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, func(c *config.Config) {
		allowPrivate(c)
		c.ImageProxyHosts = []string{"images.example.com"}
	})
	img := up.URL + "/a.png" // host 127.0.0.1: not on the list
	c := e.create(withRefs(img))
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), img), 404, "not_found")
	if up.count("/a.png") != 0 {
		t.Fatal("non-allowlisted host fetched")
	}

	// The allowlist is re-checked on every redirect hop.
	port := strconv.Itoa(int(addrPort(t, up.URL).Port()))
	up.setRedirect("http://localhost:" + port + "/b.png")
	e2 := newEnv(t, func(c *config.Config) {
		allowPrivate(c)
		c.ImageProxyHosts = []string{"127.0.0.1"}
	})
	ok, hop := up.URL+"/c.png", up.URL+"/to-target"
	c2 := e2.create(withRefs(ok, hop))
	expectImage(t, e2.fetchImage(c2.ID, readHdr(c2.keys), ok))
	e2.expectErr(e2.fetchImage(c2.ID, readHdr(c2.keys), hop), 502, "upstream")
	if up.count("/b.png") != 0 || up.count("/to-target") != 1 {
		t.Fatalf("redirect to a non-allowlisted host followed (b=%d hop=%d)", up.count("/b.png"), up.count("/to-target"))
	}
}

func TestImageProxyBurnTicket(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, allowPrivate)
	img, other := up.URL+"/a.png", up.URL+"/b.png"
	c := e.create(func(m map[string]any) {
		m["burn"], m["discussion"], m["kdf"] = true, false, nil
		withRefs(img)(m)
	})
	// Before opening, read auth cannot be used for a burn paste's images.
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), img), 400, "invalid")
	e.expectErr(e.fetchImage(c.ID, map[string]string{"X-Read-Auth": b64(randBytes(32))}, img), 400, "invalid")

	var o map[string]any
	e.expectJSON(e.do("POST", "/api/pastes/"+c.ID+"/open", readHdr(c.keys), nil), 200, &o)
	ticket, _ := o["imageTicket"].(string)
	if raw := unb64(t, ticket); len(raw) != 32 {
		t.Fatalf("imageTicket %v", o["imageTicket"])
	}
	e.expectErr(e.do("GET", "/api/pastes/"+c.ID, nil, nil), 404, "not_found") // burned

	th := map[string]string{"X-Image-Ticket": ticket}
	expectImage(t, e.fetchImage(c.ID, th, img))
	e.expectErr(e.fetchImage(c.ID, th, other), 404, "not_found")
	e.expectErr(e.fetchImage(c.ID, map[string]string{"X-Image-Ticket": b64(randBytes(32))}, img), 403, "unauthorized")
	e.expectErr(e.fetchImage(c.ID, map[string]string{"X-Image-Ticket": "short"}, img), 403, "unauthorized")
	e.expectErr(e.fetchImage("AAAAAAAAAA", th, img), 403, "unauthorized") // bound to the paste id
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), img), 404, "not_found")

	e.clock.Advance(14 * time.Minute)
	expectImage(t, e.fetchImage(c.ID, th, img))
	e.clock.Advance(2 * time.Minute)
	e.expectErr(e.fetchImage(c.ID, th, img), 403, "unauthorized")

	// No refs → null ticket; keep pastes → null ticket (key always present).
	for _, mod := range []func(m map[string]any){
		func(m map[string]any) { m["burn"], m["discussion"] = true, false },
		withRefs(img),
	} {
		c := e.create(mod)
		var o map[string]any
		e.expectJSON(e.do("POST", "/api/pastes/"+c.ID+"/open", readHdr(c.keys), nil), 200, &o)
		if v, ok := o["imageTicket"]; !ok || v != nil {
			t.Fatalf("imageTicket = %v (present %v), want null", v, ok)
		}
	}
}

func TestImageRefsEndpoint(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, allowPrivate)
	c := e.create(nil)
	path := "/api/pastes/" + c.ID + "/image-refs"
	a, b := up.URL+"/a.png", up.URL+"/b.png"
	body := func(urls ...string) map[string]any {
		refs := []string{}
		for _, u := range urls {
			refs = append(refs, imgRef(u))
		}
		return map[string]any{"imageRefs": refs}
	}
	expect204 := func(resp *http.Response) {
		t.Helper()
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Fatalf("status %d, want 204", resp.StatusCode)
		}
	}

	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), a), 404, "not_found")
	// Owner token alone is enough.
	expect204(e.doJSON("POST", path, ownerHdr(c.keys), body(a)))
	expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), a))
	// Guests with read auth, while the discussion is open.
	expect204(e.doJSON("POST", path, readHdr(c.keys), body(b, b)))
	expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), b))
	expect204(e.doJSON("POST", path, readHdr(c.keys), map[string]any{"imageRefs": []string{}}))

	e.expectErr(e.doJSON("POST", path, nil, body(a)), 403, "unauthorized")
	e.expectErr(e.doJSON("POST", path, map[string]string{"X-Read-Auth": b64(randBytes(32))}, body(a)), 403, "unauthorized")
	e.expectErr(e.doJSON("POST", path, merge(readHdr(c.keys), map[string]string{"X-Owner-Token": b64(randBytes(32))}), body(a)), 403, "forbidden")
	e.expectErr(e.doJSON("POST", path, readHdr(c.keys), map[string]any{}), 400, "invalid")
	e.expectErr(e.doJSON("POST", path, readHdr(c.keys), map[string]any{"imageRefs": []string{"nope"}}), 400, "invalid")
	e.expectErr(e.doJSON("POST", path, readHdr(c.keys), map[string]any{"imageRefs": []string{b64(randBytes(31))}}), 400, "invalid")
	many := []string{}
	for i := 0; i < 101; i++ {
		many = append(many, b64(randBytes(32)))
	}
	e.expectErr(e.doJSON("POST", path, readHdr(c.keys), map[string]any{"imageRefs": many}), 413, "too_large")

	// Locked: guests refused, owner fine.
	e.expectJSON(e.doJSON("POST", "/api/pastes/"+c.ID+"/lock", ownerHdr(c.keys), map[string]any{"locked": true}), 200, nil)
	e.expectErr(e.doJSON("POST", path, readHdr(c.keys), body(up.URL+"/c.png")), 403, "locked")
	expect204(e.doJSON("POST", path, ownerHdr(c.keys), body(up.URL+"/c.png")))

	// Without a discussion only the owner may register (answered before auth).
	nd := e.create(func(m map[string]any) { m["discussion"] = false })
	ndPath := "/api/pastes/" + nd.ID + "/image-refs"
	e.expectErr(e.doJSON("POST", ndPath, readHdr(nd.keys), body(a)), 403, "locked")
	e.expectErr(e.doJSON("POST", ndPath, map[string]string{"X-Read-Auth": b64(randBytes(32))}, body(a)), 403, "locked")
	expect204(e.doJSON("POST", ndPath, ownerHdr(nd.keys), body(a)))

	// Not for burn pastes.
	burn := e.create(func(m map[string]any) { m["burn"], m["discussion"] = true, false })
	e.expectErr(e.doJSON("POST", "/api/pastes/"+burn.ID+"/image-refs", ownerHdr(burn.keys), body(a)), 400, "invalid")
	e.expectErr(e.doJSON("POST", "/api/pastes/AAAAAAAAAA/image-refs", ownerHdr(c.keys), body(a)), 404, "not_found")
}

func TestImageRefsUnionAndCap(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, func(c *config.Config) {
		allowPrivate(c)
		c.MaxImageRefs = 5
	})
	u := func(n int) string { return fmt.Sprintf("%s/a.png?n=%d", up.URL, n) }
	refs := func(ns ...int) []string {
		out := []string{}
		for _, n := range ns {
			out = append(out, imgRef(u(n)))
		}
		return out
	}
	c := e.create(func(m map[string]any) { m["imageRefs"] = refs(1, 1) })
	put := func(base int, rs []string) *http.Response {
		return e.doJSON("PUT", "/api/pastes/"+c.ID, ownerHdr(c.keys),
			map[string]any{"content": b64(envelope(10)), "baseVersion": base, "imageRefs": rs})
	}
	// Union: refs are never removed by later edits.
	e.expectJSON(put(1, refs(2)), 200, nil)
	guest := randBytes(32)
	gh := merge(readHdr(c.keys), map[string]string{"X-Author-Token": b64(guest)})
	var cm commentT
	e.expectJSON(e.doJSON("POST", "/api/pastes/"+c.ID+"/comments", gh,
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5)), "imageRefs": refs(3)}), 201, &cm)
	e.expectJSON(e.doJSON("PUT", "/api/pastes/"+c.ID+"/comments/"+cm.ID, gh,
		map[string]any{"content": b64(envelope(5)), "baseVersion": 1, "imageRefs": refs(4, 1)}), 200, nil)
	for n := 1; n <= 4; n++ {
		expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), u(n)))
	}

	// 4 of 5 used. Over the cap → 413 and nothing of the request is written.
	e.expectErr(put(2, refs(5, 6)), 413, "too_large")
	if o := e.open(c); o.Version != 2 {
		t.Fatalf("version %d after rejected PUT", o.Version)
	}
	e.expectErr(e.doJSON("POST", "/api/pastes/"+c.ID+"/image-refs", ownerHdr(c.keys), map[string]any{"imageRefs": refs(5, 6)}), 413, "too_large")
	e.expectErr(e.doJSON("POST", "/api/pastes/"+c.ID+"/comments", gh,
		map[string]any{"id": newID(), "parentId": nil, "content": b64(envelope(5)), "imageRefs": refs(5, 6)}), 413, "too_large")
	e.expectErr(e.doJSON("PUT", "/api/pastes/"+c.ID+"/comments/"+cm.ID, gh,
		map[string]any{"content": b64(envelope(5)), "baseVersion": 2, "imageRefs": refs(5, 6)}), 413, "too_large")
	o := e.open(c)
	if len(o.Comments) != 1 || o.Comments[0].Version != 2 {
		t.Fatalf("comments after rejected requests: %+v", o.Comments)
	}
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), u(5)), 404, "not_found")
	// Re-registering known refs is free; exactly reaching the cap is fine.
	e.expectJSON(put(2, refs(1, 2, 3, 4, 5)), 200, nil)
	expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), u(5)))
	// Create over the cap.
	k := keys{read: randBytes(32), owner: randBytes(32)}
	m := defaultMeta(k)
	m["imageRefs"] = refs(1, 2, 3, 4, 5, 6)
	e.expectErr(e.createRaw(createReq{meta: m}, nil), 413, "too_large")
	m["imageRefs"] = []string{"!!"}
	e.expectErr(e.createRaw(createReq{meta: m}, nil), 400, "invalid")
}

func TestImageProxyDisabled(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.ImageProxy = false })
	img := "https://example.com/a.png"
	c := e.create(withRefs(img)) // refs are still accepted and stored
	var cfg map[string]any
	e.expectJSON(e.do("GET", "/api/config", nil, nil), 200, &cfg)
	if cfg["imageProxy"] != false {
		t.Fatalf("config %v", cfg)
	}
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), img), 404, "not_found")
	e.expectErr(e.doJSON("POST", "/api/pastes/"+c.ID+"/image-refs", ownerHdr(c.keys),
		map[string]any{"imageRefs": []string{imgRef(img)}}), 404, "not_found")
	b := e.create(func(m map[string]any) { m["burn"], m["discussion"] = true, false; withRefs(img)(m) })
	var o map[string]any
	e.expectJSON(e.do("POST", "/api/pastes/"+b.ID+"/open", readHdr(b.keys), nil), 200, &o)
	if v, ok := o["imageTicket"]; !ok || v != nil {
		t.Fatalf("imageTicket with proxy disabled: %v", v)
	}
}

func TestImageProxyRateLimit(t *testing.T) {
	up := newImageUpstream(t)
	e := newEnv(t, func(c *config.Config) {
		allowPrivate(c)
		c.RateImage = config.Rate{N: 2, Per: time.Hour}
	})
	img := up.URL + "/a.png"
	c := e.create(withRefs(img))
	expectImage(t, e.fetchImage(c.ID, readHdr(c.keys), img))
	e.expectErr(e.fetchImage(c.ID, readHdr(c.keys), up.URL+"/b.png"), 404, "not_found") // counted too
	resp := e.fetchImage(c.ID, readHdr(c.keys), img)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("Retry-After missing")
	}
	e.expectErr(resp, 429, "rate_limited")
}
