// Package api implements Hushbin's HTTP interface (DESIGN.md §4 and §5):
// the JSON API under /api, live events, the embedded SPA, health checks and
// security headers.
package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"hushbin/server/clientip"
	"hushbin/server/config"
	"hushbin/server/events"
	"hushbin/server/ratelimit"
	"hushbin/server/static"
	"hushbin/server/store"
)

// Security headers sent on every response (DESIGN.md §5).
var securityHeaders = [][2]string{
	{"Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' blob: data:; font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; manifest-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"},
	{"Referrer-Policy", "no-referrer"},
	{"X-Content-Type-Options", "nosniff"},
	{"Cross-Origin-Opener-Policy", "same-origin"},
	{"Permissions-Policy", "camera=(), microphone=(), geolocation=()"},
}

const (
	requestDeadline = 15 * time.Minute // per request read/write deadline (non-streaming)
	pingInterval    = 20 * time.Second
	streamWriteWait = 30 * time.Second
)

// Options configure a Server.
type Options struct {
	Config config.Config
	Store  *store.Store
	Hub    *events.Hub      // optional; a new hub is created when nil
	Logger *slog.Logger     // optional
	Now    func() time.Time // optional clock for rate limiters
	// PingInterval overrides the SSE keep-alive interval (tests).
	PingInterval time.Duration
}

type limiters struct {
	create, comment, upload, auth, general *ratelimit.Limiter
}

func (l *limiters) all() []*ratelimit.Limiter {
	return []*ratelimit.Limiter{l.create, l.comment, l.upload, l.auth, l.general}
}

// Server is the HTTP handler.
type Server struct {
	cfg     config.Config
	store   *store.Store
	hub     *events.Hub
	log     *slog.Logger
	ips     *clientip.Resolver
	lim     limiters
	ping    time.Duration
	handler http.Handler

	streamsMu sync.Mutex
	streams   map[string]int
}

// New builds the HTTP handler. spa is the built frontend (the contents of
// web/dist); it may be empty in tests.
func New(opts Options, spa fs.FS) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Hub == nil {
		opts.Hub = events.NewHub(64)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.PingInterval <= 0 {
		opts.PingInterval = pingInterval
	}
	c := opts.Config
	mk := func(r config.Rate) *ratelimit.Limiter { return ratelimit.New(r.N, r.Per, opts.Now) }
	s := &Server{
		cfg:   c,
		store: opts.Store,
		hub:   opts.Hub,
		log:   opts.Logger,
		ips:   clientip.New(c.TrustedProxies),
		lim: limiters{
			create:  mk(c.RateCreate),
			comment: mk(c.RateComment),
			upload:  mk(c.RateUpload),
			auth:    mk(c.RateAuth),
			general: mk(c.RateGeneral),
		},
		ping:    opts.PingInterval,
		streams: make(map[string]int),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", s.handleConfig)
	mux.HandleFunc("POST /api/pastes", s.handleCreate)
	mux.HandleFunc("GET /api/pastes/{id}", s.handleHeader)
	mux.HandleFunc("PUT /api/pastes/{id}", s.handleUpdate)
	mux.HandleFunc("DELETE /api/pastes/{id}", s.handleDelete)
	mux.HandleFunc("POST /api/pastes/{id}/open", s.handleOpen)
	mux.HandleFunc("POST /api/pastes/{id}/lock", s.handleLock)
	mux.HandleFunc("GET /api/pastes/{id}/versions", s.handleVersions)
	mux.HandleFunc("GET /api/pastes/{id}/versions/{v}", s.handleVersion)
	mux.HandleFunc("POST /api/pastes/{id}/attachments", s.handleUpload)
	mux.HandleFunc("GET /api/pastes/{id}/attachments/{aid}", s.handleDownload)
	mux.HandleFunc("POST /api/pastes/{id}/comments", s.handleCommentCreate)
	mux.HandleFunc("PUT /api/pastes/{id}/comments/{cid}", s.handleCommentUpdate)
	mux.HandleFunc("DELETE /api/pastes/{id}/comments/{cid}", s.handleCommentDelete)
	mux.HandleFunc("GET /api/pastes/{id}/comments/{cid}/versions", s.handleCommentVersions)
	mux.HandleFunc("GET /api/pastes/{id}/events", s.handleEvents)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, codeNotFound, "no such endpoint")
	})
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /share", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.Handle("/", static.New(spa))

	s.handler = s.middleware(mux)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Hub returns the event hub.
func (s *Server) Hub() *events.Hub { return s.hub }

// Shutdown ends all live event streams (call from http.Server.RegisterOnShutdown).
func (s *Server) Shutdown() { s.hub.Close() }

// Run performs periodic maintenance until ctx is cancelled: the expiry sweep
// and rate-limiter eviction every minute, temp-file cleanup hourly.
func (s *Server) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	n := 0
	for {
		s.Sweep(ctx)
		for _, l := range s.lim.all() {
			l.Sweep()
		}
		if n%60 == 0 {
			if err := s.store.CleanTemp(time.Hour); err != nil {
				s.log.Warn("temp cleanup failed", "err", err)
			}
		}
		n++
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Sweep deletes expired pastes and notifies their subscribers.
func (s *Server) Sweep(ctx context.Context) {
	ids, err := s.store.SweepExpired(ctx)
	for _, id := range ids {
		s.hub.Publish(id, events.PasteDeleted, evPasteDeleted{Type: events.PasteDeleted})
	}
	if err != nil && ctx.Err() == nil {
		s.log.Error("expiry sweep failed", "err", err)
	}
	if len(ids) > 0 {
		s.log.Info("expired pastes swept", "count", len(ids))
	}
}

// ---------------------------------------------------------------------------
// Middleware

type ctxKey int

const ipKeyCtx ctxKey = 1

type reqInfo struct {
	ip  netip.Addr
	key string // rate-limit key
}

func info(r *http.Request) reqInfo {
	if v, ok := r.Context().Value(ipKeyCtx).(reqInfo); ok {
		return v
	}
	return reqInfo{key: "unknown"}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() { _ = w.FlushError() }

// FlushError lets http.ResponseController report flush failures.
func (w *statusWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		h := w.Header()
		for _, kv := range securityHeaders {
			h.Set(kv[0], kv[1])
		}
		ip := s.ips.ClientIP(r)
		ri := reqInfo{ip: ip, key: clientip.Key(ip)}
		r = r.WithContext(context.WithValue(r.Context(), ipKeyCtx, ri))

		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(start.Add(requestDeadline))
		_ = rc.SetWriteDeadline(start.Add(requestDeadline))

		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			v := recover()
			aborted := v == http.ErrAbortHandler
			if v != nil && !aborted {
				s.log.Error("panic", "err", fmt.Sprint(v), "path", r.URL.Path, "stack", string(debug.Stack()))
				if sw.status == 0 {
					writeError(sw, http.StatusInternalServerError, "internal", "internal error")
				}
			}
			level := slog.LevelInfo
			if sw.status >= 500 {
				level = slog.LevelError
			}
			s.log.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Int64("bytes", sw.bytes),
				slog.Int64("ms", time.Since(start).Milliseconds()),
				slog.String("ip", ip.String()),
				slog.Bool("aborted", aborted),
			)
			if aborted {
				panic(v) // let net/http abort the connection
			}
		}()

		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			if !s.allow(sw, r, s.lim.general) {
				return
			}
		}
		next.ServeHTTP(sw, r)
	})
}

// allow consumes a token from l for the client, writing 429 when exhausted.
func (s *Server) allow(w http.ResponseWriter, r *http.Request, l *ratelimit.Limiter) bool {
	ok, wait := l.Allow(info(r).key)
	if !ok {
		rateLimited(w, wait)
	}
	return ok
}

func rateLimited(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetryAfterSeconds(wait)))
	writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many requests, slow down")
}

// acquireStream reserves one of the client's concurrent event streams.
func (s *Server) acquireStream(key string) bool {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if s.streams[key] >= s.cfg.MaxStreamsPerIP {
		return false
	}
	s.streams[key]++
	return true
}

func (s *Server) releaseStream(key string) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if s.streams[key] <= 1 {
		delete(s.streams, key)
	} else {
		s.streams[key]--
	}
}

// ---------------------------------------------------------------------------
// Misc handlers

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.store.Ping(ctx); err != nil {
		s.log.Error("health check failed", "err", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
		return
	}
	_, _ = w.Write([]byte("ok"))
}

type configJSON struct {
	Version             string  `json:"version"`
	MaxBody             int64   `json:"maxBody"`
	MaxComment          int64   `json:"maxComment"`
	MaxAttachment       int64   `json:"maxAttachment"`
	MaxPasteAttachments int64   `json:"maxPasteAttachments"`
	Expiries            []int64 `json:"expiries"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, configJSON{
		Version:             config.Version,
		MaxBody:             s.cfg.MaxBody,
		MaxComment:          s.cfg.MaxComment,
		MaxAttachment:       s.cfg.MaxAttachment,
		MaxPasteAttachments: s.cfg.MaxPasteAttachments,
		Expiries:            s.cfg.Expiries(),
	})
}

// fail writes err as an API error response.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeAPIError(w, ae)
		return
	}
	var se *store.Error
	if errors.As(err, &se) {
		ae := &apiError{code: se.Code, msg: se.Message}
		switch se.Code {
		case store.CodeInvalid:
			ae.status = http.StatusBadRequest
		case store.CodeForbidden, store.CodeLocked:
			ae.status = http.StatusForbidden
		case store.CodeNotFound:
			ae.status = http.StatusNotFound
		case store.CodeConflict:
			ae.status = http.StatusConflict
			if se.HasVersion {
				v := se.Version
				ae.version = &v
			}
		case store.CodeTooLarge:
			ae.status = http.StatusRequestEntityTooLarge
		default:
			ae.status = http.StatusInternalServerError
		}
		writeAPIError(w, ae)
		return
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, "request body too large")
		return
	}
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		return // client went away
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal error")
}
