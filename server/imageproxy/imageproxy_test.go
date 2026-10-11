package imageproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsPublicAddr(t *testing.T) {
	cases := []struct {
		addr   string
		public bool
	}{
		// Public.
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"93.184.216.34", true},
		{"100.63.255.255", true}, // just below CGNAT
		{"100.128.0.0", true},    // just above CGNAT
		{"172.15.255.255", true},
		{"172.32.0.0", true},
		{"2606:4700:4700::1111", true},
		{"2a00:1450:4001:80b::200e", true},
		{"::ffff:8.8.8.8", true},  // IPv4-mapped public
		{"2002:808:808::1", true}, // 6to4 embedding 8.8.8.8
		// IPv4 special ranges.
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"10.0.0.1", false},
		{"10.255.255.255", false},
		{"100.64.0.1", false},
		{"100.127.255.254", false},
		{"127.0.0.1", false},
		{"127.255.255.254", false},
		{"169.254.169.254", false}, // cloud metadata
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"192.0.0.8", false},
		{"192.0.2.1", false},
		{"192.88.99.1", false},
		{"192.168.1.1", false},
		{"198.18.0.1", false},
		{"198.19.255.255", false},
		{"198.51.100.7", false},
		{"203.0.113.9", false},
		{"224.0.0.1", false},
		{"239.255.255.250", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		// IPv4-mapped forms of private addresses.
		{"::ffff:127.0.0.1", false},
		{"::ffff:10.0.0.1", false},
		{"::ffff:169.254.169.254", false},
		{"::ffff:100.64.0.1", false},
		{"::ffff:0.0.0.0", false},
		// IPv6 special ranges.
		{"::", false},
		{"::1", false},
		{"::127.0.0.1", false},        // IPv4-compatible
		{"::ffff:0:a00:1", false},     // IPv4-translated 10.0.0.1
		{"64:ff9b::7f00:1", false},    // NAT64 embedding 127.0.0.1
		{"64:ff9b::a9fe:a9fe", false}, // NAT64 embedding 169.254.169.254
		{"64:ff9b::808:808", false},   // NAT64 is refused altogether
		{"64:ff9b:1::1", false},
		{"100::1", false},
		{"2001::1", false},          // Teredo
		{"2001:2::1", false},        // benchmarking
		{"2001:db8::1", false},      // documentation
		{"2002:7f00:1::1", false},   // 6to4 embedding 127.0.0.1
		{"2002:c0a8:101::1", false}, // 6to4 embedding 192.168.1.1
		{"3fff::1", false},
		{"5f00::1", false},
		{"fc00::1", false},
		{"fd12:3456:789a::1", false},
		{"fe80::1", false},
		{"fe80::1%eth0", false},
		{"fec0::1", false},
		{"ff02::1", false},
		{"ff0e::1", false},
		{"4000::1", false}, // outside 2000::/3: not global unicast
	}
	for _, tc := range cases {
		a, err := netip.ParseAddr(tc.addr)
		if err != nil {
			t.Fatalf("%s: %v", tc.addr, err)
		}
		if got := IsPublicAddr(a); got != tc.public {
			t.Errorf("IsPublicAddr(%s) = %v, want %v", tc.addr, got, tc.public)
		}
	}
	if IsPublicAddr(netip.Addr{}) {
		t.Error("invalid address must not be public")
	}
	if v4, ok := EmbeddedIPv4(netip.MustParseAddr("64:ff9b::7f00:1")); !ok || v4.String() != "127.0.0.1" {
		t.Errorf("embedded NAT64 %v %v", v4, ok)
	}
	if v4, ok := EmbeddedIPv4(netip.MustParseAddr("2002:c0a8:101::1")); !ok || v4.String() != "192.168.1.1" {
		t.Errorf("embedded 6to4 %v %v", v4, ok)
	}
}

func pad(b []byte, n int) []byte { return append(b, make([]byte, n)...) }

func bmp(dib uint32) []byte {
	b := make([]byte, 54)
	copy(b, "BM")
	binary.LittleEndian.PutUint32(b[14:], dib)
	return b
}

func ftyp(major string, compat ...string) []byte {
	b := make([]byte, 16+4*len(compat))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], "ftyp")
	copy(b[8:], major)
	for i, c := range compat {
		copy(b[16+4*i:], c)
	}
	return pad(b, 32)
}

func TestSniff(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"png", pad(append([]byte{}, pngSig...), 20), "image/png"},
		{"jpeg", pad([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 20), "image/jpeg"},
		{"gif87", pad([]byte("GIF87a"), 20), "image/gif"},
		{"gif89", pad([]byte("GIF89a"), 20), "image/gif"},
		{"webp", pad([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), 20), "image/webp"},
		{"bmp", bmp(40), "image/bmp"},
		{"bmp v5", bmp(124), "image/bmp"},
		{"avif", ftyp("avif", "mif1", "miaf"), "image/avif"},
		{"avis", ftyp("avis"), "image/avif"},
		{"avif via mif1", ftyp("mif1", "miaf", "avif"), "image/avif"},
		{"heic", ftyp("mif1", "heic"), ""},
		{"mp4", ftyp("isom", "mp41"), ""},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ""},
		{"svg with xml decl", []byte(`<?xml version="1.0"?><svg/>`), ""},
		{"html", []byte("<!doctype html><html><body>hi</body></html>"), ""},
		{"text starting BM", []byte("BMW is a car brand, not a bitmap at all"), ""},
		{"riff wave", pad([]byte("RIFF\x10\x00\x00\x00WAVEfmt "), 20), ""},
		{"too short png", append([]byte{}, pngSig[:7]...), ""},
		{"empty", nil, ""},
		{"json", []byte(`{"image":"png"}`), ""},
	}
	for _, tc := range cases {
		if got := Sniff(tc.data); got != tc.want {
			t.Errorf("%s: Sniff = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestValidateURL(t *testing.T) {
	p := New(Config{})
	good := []string{
		"https://example.com/a.png",
		"http://example.com/a.png",
		"https://example.com:443/a.png",
		"http://example.com:80/a.png",
		"https://xn--bcher-kva.example/a.png",
		"https://img.example.com/a%20b.png?x=1&y=2#frag",
		"https://1.2.3.4/a.png",
		"https://[2606:4700::1]/a.png",
		"HTTPS://Example.COM/a.png",
		"https://under_score.example.com/a.png",
	}
	for _, u := range good {
		if _, err := p.ValidateURL(u); err != nil {
			t.Errorf("%q rejected: %v", u, err)
		}
	}
	bad := []string{
		"",
		"ftp://example.com/a.png",
		"file:///etc/passwd",
		"gopher://example.com/",
		"javascript:alert(1)",
		"data:image/png;base64,iVBORw0KGgo=",
		"//example.com/a.png",
		"/relative.png",
		"example.com/a.png",
		"http:example.com/a.png",
		"https://user:pw@example.com/a.png",
		"https://user@example.com/a.png",
		"https://example.com:8080/a.png",
		"http://example.com:443/a.png",
		"https://example.com:80/a.png",
		"https://bücher.example/a.png",
		"https:///a.png",
		"https://exa mple.com/a.png",
		"https://example.com/" + strings.Repeat("a", MaxURLLen),
	}
	for _, u := range bad {
		if _, err := p.ValidateURL(u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	// ALLOW_PRIVATE lifts the port rule (but not the scheme/userinfo rules).
	lax := New(Config{AllowPrivate: true})
	if _, err := lax.ValidateURL("http://127.0.0.1:8080/a.png"); err != nil {
		t.Error(err)
	}
	if _, err := lax.ValidateURL("https://u@127.0.0.1:8080/a.png"); err == nil {
		t.Error("userinfo accepted with AllowPrivate")
	}
}

func TestHostAllowlist(t *testing.T) {
	p := New(Config{Hosts: []string{"Example.com", ".imgur.com."}})
	for u, want := range map[string]bool{
		"https://example.com/a":          true,
		"https://i.example.com/a":        true,
		"https://a.b.EXAMPLE.com./a":     true,
		"https://i.imgur.com/a":          true,
		"https://imgur.com/a":            true,
		"https://badexample.com/a":       false,
		"https://example.com.evil.net/a": false,
		"https://example.org/a":          false,
		"https://1.2.3.4/a":              false,
	} {
		parsed, err := p.ValidateURL(u)
		if err != nil {
			t.Fatal(u, err)
		}
		if got := p.HostAllowed(parsed); got != want {
			t.Errorf("HostAllowed(%s) = %v, want %v", u, got, want)
		}
	}
	if !New(Config{}).HostAllowed(mustURL(t, p, "https://anything.example/a")) {
		t.Error("empty allowlist must allow any host")
	}
}

func mustURL(t *testing.T, p *Proxy, s string) *url.URL {
	t.Helper()
	u, err := p.ValidateURL(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

var pngBytes = pad(append([]byte{}, pngSig...), 100)

func TestDialerBlocksPrivateAddresses(t *testing.T) {
	p := New(Config{})
	for _, u := range []string{
		"http://127.0.0.1/a.png",
		"http://localhost/a.png",
		"http://[::1]/a.png",
		"http://10.0.0.1/a.png",
		"http://169.254.169.254/latest/meta-data",
		"http://[::ffff:127.0.0.1]/a.png",
		"http://[64:ff9b::7f00:1]/a.png",
		"https://0.0.0.0/a.png",
	} {
		_, err := p.Get(context.Background(), u)
		if err == nil || err.Code != CodeUpstream {
			t.Errorf("%s: got %v, want upstream error", u, err)
		}
	}
	// The control hook itself: ports other than 80/443 are refused even for
	// public addresses.
	if p.control("tcp4", "8.8.8.8:22", nil) == nil {
		t.Error("port 22 allowed")
	}
	if p.control("tcp4", "8.8.8.8:443", nil) != nil || p.control("tcp6", "[2606:4700::1]:80", nil) != nil {
		t.Error("public address refused")
	}
	if p.control("tcp4", "127.0.0.1:443", nil) == nil {
		t.Error("loopback allowed")
	}
}

type upstream struct {
	*httptest.Server
	mu   sync.Mutex
	hits map[string]int
	reqs []*http.Request
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	u := &upstream{hits: map[string]int{}}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.hits[r.URL.Path]++
		u.reqs = append(u.reqs, r.Clone(context.Background()))
		u.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) count(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[path]
}

func TestFetchHeadersAndRedirectHygiene(t *testing.T) {
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.SetCookie(w, &http.Cookie{Name: "track", Value: "1"})
			http.Redirect(w, r, "/img", http.StatusFound)
		case "/img":
			w.Header().Set("Content-Type", "text/html") // ignored
			w.Write(pngBytes)
		}
	})
	p := New(Config{AllowPrivate: true, UserAgent: "Hushbin-ImageProxy/test"})
	res, err := p.Get(context.Background(), up.URL+"/start")
	if err != nil || res.ContentType != "image/png" || !bytes.Equal(res.Data, pngBytes) {
		t.Fatalf("got %v %v", res, err)
	}
	for _, r := range up.reqs {
		if r.Header.Get("User-Agent") != "Hushbin-ImageProxy/test" || r.Header.Get("Accept") != "image/*" ||
			r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" || r.Method != "GET" {
			t.Errorf("%s: headers %v", r.URL.Path, r.Header)
		}
	}
}

func TestSingleflightAndCache(t *testing.T) {
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Write(pngBytes)
	})
	now := time.Unix(1000, 0)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	p := New(Config{AllowPrivate: true, Now: clock})
	var wg sync.WaitGroup
	var okCount atomic.Int32
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := p.Get(context.Background(), up.URL+"/a.png"); err == nil && len(res.Data) == len(pngBytes) {
				okCount.Add(1)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if okCount.Load() != 10 || up.count("/a.png") != 1 || p.Fetches() != 1 {
		t.Fatalf("ok=%d hits=%d fetches=%d", okCount.Load(), up.count("/a.png"), p.Fetches())
	}
	// Cached for an hour.
	p.Get(context.Background(), up.URL+"/a.png")
	mu.Lock()
	now = now.Add(59 * time.Minute)
	mu.Unlock()
	p.Get(context.Background(), up.URL+"/a.png")
	if up.count("/a.png") != 1 {
		t.Fatal("cache miss within TTL")
	}
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	p.Get(context.Background(), up.URL+"/a.png")
	if up.count("/a.png") != 2 {
		t.Fatal("expired entry served")
	}
}

func TestBusy(t *testing.T) {
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Write(pngBytes)
	})
	p := New(Config{AllowPrivate: true, MaxConcurrent: 1, AcquireTimeout: 50 * time.Millisecond})
	done := make(chan *Error)
	go func() {
		_, err := p.Get(context.Background(), up.URL+"/slow.png")
		done <- err
	}()
	for up.count("/slow.png") == 0 {
		time.Sleep(time.Millisecond)
	}
	_, err := p.Get(context.Background(), up.URL+"/other.png")
	if err == nil || err.Code != CodeBusy || err.Status != http.StatusServiceUnavailable {
		t.Fatalf("got %v, want busy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Busy is not negatively cached.
	if _, err := p.Get(context.Background(), up.URL+"/other.png"); err != nil {
		t.Fatalf("after release: %v", err)
	}
}

func TestLRUEviction(t *testing.T) {
	now := time.Unix(0, 0)
	c := newLRU(3*(100+entryOverhead+10), func() time.Time { return now })
	img := &Result{Data: make([]byte, 100)}
	for _, k := range []string{"k1________", "k2________", "k3________"} {
		c.put(k, img, nil, time.Hour)
	}
	c.get("k1________") // most recently used
	c.put("k4________", img, nil, time.Hour)
	if _, _, ok := c.get("k2________"); ok {
		t.Fatal("LRU entry not evicted")
	}
	for _, k := range []string{"k1________", "k3________", "k4________"} {
		if _, _, ok := c.get(k); !ok {
			t.Fatalf("%s evicted", k)
		}
	}
	if _, used := c.stats(); used > c.max {
		t.Fatalf("over budget: %d > %d", used, c.max)
	}
	c.put("huge", &Result{Data: make([]byte, 10000)}, nil, time.Hour)
	if _, _, ok := c.get("huge"); ok {
		t.Fatal("entry larger than the budget cached")
	}
	now = now.Add(2 * time.Hour)
	c.sweep()
	if n, used := c.stats(); n != 0 || used != 0 {
		t.Fatalf("sweep left %d entries, %d bytes", n, used)
	}
}
