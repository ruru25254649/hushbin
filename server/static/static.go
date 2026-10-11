// Package static serves the built single-page app (DESIGN.md §5).
package static

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

const (
	cacheImmutable = "public, max-age=31536000, immutable"
	cacheNoCache   = "no-cache"
)

var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".htm":         "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".wasm":        "application/wasm",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".otf":         "font/otf",
	".txt":         "text/plain; charset=utf-8",
	".xml":         "application/xml",
}

// ContentType returns the media type for a file name.
func ContentType(name string) string {
	ext := strings.ToLower(path.Ext(name))
	if ct, ok := contentTypes[ext]; ok {
		return ct
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// Handler serves files from an SPA build directory.
type Handler struct {
	fsys  fs.FS
	etags sync.Map // name -> etag string
}

// New returns a handler serving spa (the contents of web/dist).
//
//   - GET/HEAD only.
//   - Existing files are served with a correct Content-Type; files under
//     assets/ (content-hashed by Vite) are cached immutably, everything else
//     (index.html, sw.js, manifest.webmanifest, icons, ...) is "no-cache"
//     and revalidated with an ETag.
//   - A missing file under assets/ is a plain 404.
//   - Any other path that is not a file ("/", "/p/<id>", ...) gets
//     index.html so the client router can handle it.
func New(spa fs.FS) *Handler { return &Handler{fsys: spa} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if h.isFile(name) {
		h.serveFile(w, r, name)
		return
	}
	if name == "assets" || strings.HasPrefix(name, "assets/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !h.isFile("index.html") {
		http.Error(w, "frontend not built", http.StatusNotFound)
		return
	}
	h.serveFile(w, r, "index.html")
}

func (h *Handler) isFile(name string) bool {
	if !fs.ValidPath(name) {
		return false
	}
	st, err := fs.Stat(h.fsys, name)
	return err == nil && st.Mode().IsRegular()
}

func (h *Handler) etag(name string) (string, error) {
	if v, ok := h.etags.Load(name); ok {
		return v.(string), nil
	}
	f, err := h.fsys.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	tag := `"` + hex.EncodeToString(sum.Sum(nil)[:12]) + `"`
	h.etags.Store(name, tag)
	return tag, nil
}

func (h *Handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	tag, err := h.etag(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	f, err := h.fsys.Open(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	var rs io.ReadSeeker
	if s, ok := f.(io.ReadSeeker); ok {
		rs = s
	} else {
		b, err := io.ReadAll(f)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(b)
	}
	hdr := w.Header()
	hdr.Set("Content-Type", ContentType(name))
	hdr.Set("ETag", tag)
	if strings.HasPrefix(name, "assets/") {
		hdr.Set("Cache-Control", cacheImmutable)
	} else {
		hdr.Set("Cache-Control", cacheNoCache)
	}
	http.ServeContent(w, r, name, time.Time{}, rs)
}
