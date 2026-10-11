// Package imageproxy fetches remote images on behalf of readers (DESIGN.md
// §4 "Remote image proxy") without becoming a general-purpose proxy:
//
//   - only http/https URLs on default ports, without userinfo, optionally
//     restricted to an allowlist of host suffixes (re-checked on every
//     redirect, at most 3);
//   - every connection, including redirects and DNS re-resolutions, is
//     checked in the dialer: the resolved address must be public unicast and
//     the port 80 or 443, so DNS rebinding cannot reach internal services;
//   - the environment's HTTP(S)_PROXY is ignored; no cookies, no Referer;
//   - only bodies whose magic bytes identify PNG, JPEG, GIF, WebP, BMP or
//     AVIF are returned, never more than MaxBytes.
//
// Which URLs may be requested at all (registered image refs) is decided by
// the caller.
package imageproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Error is a failure to deliver an image, mapped to an API error.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// cacheable reports whether the outcome should be remembered (negative
// caching); transient local conditions such as "busy" are not.
func (e *Error) cacheable() bool { return e.Code != CodeBusy && e.Code != codeGone }

// Error codes.
const (
	CodeUpstream = "upstream"
	CodeTooLarge = "too_large"
	CodeNotImage = "not_image"
	CodeBusy     = "busy"
	codeGone     = "gone" // the caller went away while waiting
)

func errUpstream(msg string) *Error {
	return &Error{Status: http.StatusBadGateway, Code: CodeUpstream, Message: msg}
}

// Result is a fetched image.
type Result struct {
	Data        []byte
	ContentType string
}

// Config configures a Proxy. Zero values get the documented defaults.
type Config struct {
	MaxBytes       int64         // largest image (default 10 MiB)
	CacheBytes     int64         // cache budget (default 64 MiB)
	Hosts          []string      // lowercase domain suffixes; empty = any host
	AllowPrivate   bool          // skip address and port checks (testing only)
	UserAgent      string        // default "Hushbin-ImageProxy"
	MaxConcurrent  int           // concurrent upstream fetches (default 8)
	AcquireTimeout time.Duration // wait for a fetch slot (default 5 s)
	DialTimeout    time.Duration // default 5 s
	FetchTimeout   time.Duration // whole fetch incl. redirects (default 10 s)
	TTL            time.Duration // cache lifetime of images (default 1 h)
	NegativeTTL    time.Duration // cache lifetime of failures (default 5 min)
	MaxRedirects   int           // default 3
	Now            func() time.Time
	Logger         *slog.Logger

	// Test hooks (not configurable from the environment). Exempt lists
	// addresses (ip:port) that bypass the address and port checks while
	// every other destination is still checked; ExtraPorts are accepted in
	// addition to 80/443 by both the URL and the dial rules, so tests can
	// show that the address rule alone stops a reachable local upstream.
	Exempt     []netip.AddrPort
	ExtraPorts []uint16
}

// Proxy fetches and caches images.
type Proxy struct {
	cfg    Config
	client *http.Client
	cache  *lru
	sem    chan struct{}

	mu       sync.Mutex
	inflight map[string]*call

	fetches atomic.Int64
}

type call struct {
	done chan struct{}
	res  *Result
	err  *Error
}

// ErrBlockedAddress is returned by the dialer for forbidden destinations.
var ErrBlockedAddress = errors.New("destination address not allowed")

// New returns a Proxy.
func New(cfg Config) *Proxy {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 10 << 20
	}
	if cfg.CacheBytes <= 0 {
		cfg.CacheBytes = 64 << 20
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "Hushbin-ImageProxy"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 8
	}
	if cfg.AcquireTimeout <= 0 {
		cfg.AcquireTimeout = 5 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = 10 * time.Second
	}
	if cfg.TTL <= 0 {
		cfg.TTL = time.Hour
	}
	if cfg.NegativeTTL <= 0 {
		cfg.NegativeTTL = 5 * time.Minute
	}
	if cfg.MaxRedirects <= 0 {
		cfg.MaxRedirects = 3
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	hosts := make([]string, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		h = strings.Trim(strings.ToLower(strings.TrimSpace(h)), ".")
		if h != "" {
			hosts = append(hosts, h)
		}
	}
	cfg.Hosts = hosts

	p := &Proxy{
		cfg:      cfg,
		cache:    newLRU(cfg.CacheBytes, cfg.Now),
		sem:      make(chan struct{}, cfg.MaxConcurrent),
		inflight: make(map[string]*call),
	}
	dialer := &net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: 30 * time.Second, Control: p.control}
	transport := &http.Transport{
		Proxy:                  nil, // never use HTTP(S)_PROXY
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  5 * time.Second,
		ExpectContinueTimeout:  time.Second,
		MaxIdleConns:           16,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	p.client = &http.Client{
		Transport:     transport,
		CheckRedirect: p.checkRedirect,
		// No Jar: cookies are never stored or sent.
	}
	return p
}

// Fetches returns the number of upstream fetches started (for tests and
// diagnostics).
func (p *Proxy) Fetches() int64 { return p.fetches.Load() }

// Sweep drops expired cache entries.
func (p *Proxy) Sweep() { p.cache.sweep() }

// control runs for every connection attempt, after DNS resolution and
// before connecting: the only place where the real destination is known.
func (p *Proxy) control(network, address string, _ syscall.RawConn) error {
	if p.cfg.AllowPrivate {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return ErrBlockedAddress
	}
	addr := ap.Addr().WithZone("").Unmap()
	for _, e := range p.cfg.Exempt {
		if e.Addr().Unmap() == addr && e.Port() == ap.Port() {
			return nil
		}
	}
	if !p.portAllowed(ap.Port()) {
		return ErrBlockedAddress
	}
	if !IsPublicAddr(addr) {
		return ErrBlockedAddress
	}
	return nil
}

func (p *Proxy) portAllowed(port uint16) bool {
	if port == 80 || port == 443 {
		return true
	}
	for _, x := range p.cfg.ExtraPorts {
		if x == port {
			return true
		}
	}
	return false
}

func (p *Proxy) exemptHostPort(host, port string) bool {
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return false
	}
	ap := netip.AddrPortFrom(a.Unmap(), uint16(n))
	for _, e := range p.cfg.Exempt {
		if e.Addr().Unmap() == ap.Addr() && e.Port() == ap.Port() {
			return true
		}
	}
	return false
}

// MaxURLLen is the longest accepted image URL.
const MaxURLLen = 2048

// ValidateURL checks an image URL: absolute http/https, at most 2048 bytes,
// a non-empty ASCII host, no userinfo, and no port other than the scheme's
// default (unless private destinations are allowed).
func (p *Proxy) ValidateURL(raw string) (*url.URL, error) {
	if raw == "" || len(raw) > MaxURLLen {
		return nil, fmt.Errorf("url must be 1-%d bytes", MaxURLLen)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("url does not parse")
	}
	if err := p.checkHop(u); err != nil {
		return nil, err
	}
	return u, nil
}

// checkHop applies the syntactic rules to the initial URL and to every
// redirect target. (The address rules are enforced by the dialer.)
func (p *Proxy) checkHop(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must be http or https")
	}
	if u.Opaque != "" || !u.IsAbs() {
		return errors.New("url must be absolute")
	}
	if u.User != nil {
		return errors.New("url must not contain credentials")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("url must have a host")
	}
	if !validHost(host) {
		return errors.New("url host must be ASCII (punycode)")
	}
	if port := u.Port(); port != "" && !p.cfg.AllowPrivate {
		def := map[string]string{"http": "80", "https": "443"}[u.Scheme]
		n, _ := strconv.ParseUint(port, 10, 16)
		if port != def && !p.exemptHostPort(host, port) && !(n != 80 && n != 443 && p.portAllowed(uint16(n))) {
			return errors.New("url must use the default port")
		}
	}
	return nil
}

func validHost(h string) bool {
	if _, err := netip.ParseAddr(h); err == nil {
		return true
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

// HostAllowed applies the HB_IMAGE_PROXY_HOSTS allowlist (exact host or
// dot-suffix match, case-insensitive). An empty list allows every host.
func (p *Proxy) HostAllowed(u *url.URL) bool {
	if len(p.cfg.Hosts) == 0 {
		return true
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	for _, s := range p.cfg.Hosts {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	return false
}

var errTooManyRedirects = errors.New("too many redirects")

func (p *Proxy) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > p.cfg.MaxRedirects {
		return errTooManyRedirects
	}
	if err := p.checkHop(req.URL); err != nil {
		return err
	}
	if !p.HostAllowed(req.URL) {
		return errors.New("redirect to a host that is not allowed")
	}
	// net/http adds a Referer on redirects and may forward headers; reset.
	req.Header.Del("Referer")
	req.Header.Del("Cookie")
	req.Header.Del("Authorization")
	p.setHeaders(req)
	return nil
}

func (p *Proxy) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", p.cfg.UserAgent)
	req.Header.Set("Accept", "image/*")
}

// Get returns the image at rawURL, which the caller has validated with
// ValidateURL and authorized. Results (and failures) are cached; concurrent
// requests for the same URL share one upstream fetch.
func (p *Proxy) Get(ctx context.Context, rawURL string) (*Result, *Error) {
	for {
		res, err, retry := p.get(ctx, rawURL)
		if !retry {
			return res, err
		}
	}
}

func (p *Proxy) get(ctx context.Context, rawURL string) (*Result, *Error, bool) {
	if res, err, ok := p.cache.get(rawURL); ok {
		return res, err, false
	}
	gone := &Error{Status: http.StatusServiceUnavailable, Code: codeGone, Message: "request cancelled"}
	p.mu.Lock()
	if c, ok := p.inflight[rawURL]; ok {
		p.mu.Unlock()
		select {
		case <-c.done:
			// The leader's own client left before a fetch slot freed up:
			// try again on our own behalf.
			if c.err != nil && c.err.Code == codeGone && ctx.Err() == nil {
				return nil, nil, true
			}
			return c.res, c.err, false
		case <-ctx.Done():
			return nil, gone, false
		}
	}
	c := &call{done: make(chan struct{})}
	p.inflight[rawURL] = c
	p.mu.Unlock()

	c.res, c.err = p.fetchSlot(ctx, rawURL)
	switch {
	case c.err == nil:
		p.cache.put(rawURL, c.res, nil, p.cfg.TTL)
	case c.err.cacheable():
		p.cache.put(rawURL, nil, c.err, p.cfg.NegativeTTL)
	}
	p.mu.Lock()
	delete(p.inflight, rawURL)
	p.mu.Unlock()
	close(c.done)
	return c.res, c.err, false
}

// fetchSlot waits (bounded) for one of the MaxConcurrent upstream slots.
func (p *Proxy) fetchSlot(ctx context.Context, rawURL string) (*Result, *Error) {
	t := time.NewTimer(p.cfg.AcquireTimeout)
	defer t.Stop()
	select {
	case p.sem <- struct{}{}:
	case <-t.C:
		return nil, &Error{Status: http.StatusServiceUnavailable, Code: CodeBusy, Message: "image proxy busy, try again"}
	case <-ctx.Done():
		return nil, &Error{Status: http.StatusServiceUnavailable, Code: codeGone, Message: "request cancelled"}
	}
	defer func() { <-p.sem }()
	return p.fetch(rawURL)
}

// fetch performs one upstream request. It is detached from the caller's
// context (other callers may be waiting on it) and bounded by FetchTimeout.
func (p *Proxy) fetch(rawURL string) (*Result, *Error) {
	p.fetches.Add(1)
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.FetchTimeout)
	defer cancel()
	log := p.cfg.Logger
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, errUpstream("bad upstream request")
	}
	p.setHeaders(req)
	resp, err := p.client.Do(req)
	if err != nil {
		// URLs are user content: only at debug level.
		log.Debug("image fetch failed", "url", rawURL, "err", err)
		switch {
		case errors.Is(err, ErrBlockedAddress):
			return nil, errUpstream("image host resolves to a forbidden address")
		case errors.Is(err, errTooManyRedirects):
			return nil, errUpstream("too many redirects")
		case errors.Is(err, context.DeadlineExceeded):
			return nil, errUpstream("upstream timed out")
		}
		return nil, errUpstream("upstream request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Debug("image fetch status", "url", rawURL, "status", resp.StatusCode)
		return nil, errUpstream(fmt.Sprintf("upstream answered %d", resp.StatusCode))
	}
	tooLarge := &Error{Status: http.StatusRequestEntityTooLarge, Code: CodeTooLarge, Message: "image is too large"}
	if resp.ContentLength > p.cfg.MaxBytes {
		return nil, tooLarge
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.cfg.MaxBytes+1))
	if err != nil {
		log.Debug("image read failed", "url", rawURL, "err", err)
		return nil, errUpstream("reading the upstream response failed")
	}
	if int64(len(data)) > p.cfg.MaxBytes {
		return nil, tooLarge
	}
	ct := Sniff(data)
	if ct == "" {
		return nil, &Error{Status: http.StatusUnsupportedMediaType, Code: CodeNotImage, Message: "not a supported image (PNG, JPEG, GIF, WebP, AVIF, BMP)"}
	}
	return &Result{Data: data, ContentType: ct}, nil
}
