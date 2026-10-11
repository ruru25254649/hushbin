package api

import (
	"crypto/sha256"
	"net/http"
	"strconv"

	"hushbin/server/store"
)

const (
	hdrImageTicket       = "X-Image-Ticket"
	maxImageRefsPerReq   = 100
	imageRequestLimit    = 8 << 10  // POST /image body
	imageRefsRequestBody = 16 << 10 // POST /image-refs body (100 refs ≈ 4.5 KiB)
)

// parseImageRefs decodes the optional "imageRefs" array: at most 100
// entries, each the strict b64u encoding of 32 bytes; duplicates dropped.
func parseImageRefs(in []string) ([][]byte, *apiError) {
	if len(in) > maxImageRefsPerReq {
		return nil, errTooBig("at most 100 imageRefs per request")
	}
	out := make([][]byte, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		b, ok := unb64(s)
		if !ok || len(b) != store.ImageRefLen {
			return nil, errBad("imageRefs must be base64url SHA-256 digests")
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, b)
	}
	return out, nil
}

// POST /api/pastes/{id}/image-refs — register refs for a live preview.
// Owner token, or read auth while the discussion is enabled and unlocked.
func (s *Server) handleImageRefs(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.ImageProxy {
		writeError(w, http.StatusNotFound, codeNotFound, "image proxy is disabled")
		return
	}
	p, ok := s.loadPaste(w, r)
	if !ok {
		return
	}
	if p.Burn {
		writeAPIError(w, errBad("not available for burn-after-reading pastes"))
		return
	}
	isOwner, _, ok := s.optionalOwner(w, r, p)
	if !ok {
		return
	}
	if !isOwner {
		// Guests register refs only for comments; the discussion flag is
		// public, so answer before checking read auth.
		if !p.Discussion {
			writeError(w, http.StatusForbidden, codeLocked, "discussion is disabled")
			return
		}
		if !s.requireReadAuth(w, r, p) {
			return
		}
	}
	var body struct {
		ImageRefs *[]string `json:"imageRefs"`
	}
	if aerr := decodeJSON(w, r, imageRefsRequestBody, &body); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if body.ImageRefs == nil {
		writeAPIError(w, errBad("imageRefs is required"))
		return
	}
	refs, aerr := parseImageRefs(*body.ImageRefs)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	err := s.store.AddImageRefs(r.Context(), p.ID, refs, func(p *store.Paste) error {
		if isOwner {
			return nil
		}
		if !p.Discussion {
			return store.ErrLocked("discussion is disabled")
		}
		if p.Locked {
			return store.ErrLocked("discussion is locked")
		}
		return nil
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/pastes/{id}/image — fetch a registered remote image.
func (s *Server) handleImage(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.ImageProxy {
		writeError(w, http.StatusNotFound, codeNotFound, "image proxy is disabled")
		return
	}
	if !s.allow(w, r, s.lim.image) {
		return
	}

	// Which URLs may be fetched: a burn ticket's snapshot, or the live
	// paste's registered set (read auth).
	var registered func(ref []byte) (bool, error)
	if h := r.Header.Get(hdrImageTicket); h != "" {
		var t *imageTicket
		if raw, ok := parseToken(h); ok {
			t = s.tickets.lookup(r.PathValue("id"), raw)
		}
		if t == nil {
			writeError(w, http.StatusForbidden, codeUnauthorized, "invalid or expired image ticket")
			return
		}
		registered = func(ref []byte) (bool, error) { return t.has(ref), nil }
	} else {
		p, ok := s.loadPaste(w, r)
		if !ok {
			return
		}
		if p.Burn {
			// Burn pastes are only readable through /open (which mints a
			// ticket); no password oracle that avoids burning.
			writeAPIError(w, errBad("burn-after-reading pastes need X-Image-Ticket"))
			return
		}
		if !s.requireReadAuth(w, r, p) {
			return
		}
		registered = func(ref []byte) (bool, error) { return s.store.HasImageRef(r.Context(), p.ID, ref) }
	}

	var body struct {
		URL *string `json:"url"`
	}
	if aerr := decodeJSON(w, r, imageRequestLimit, &body); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if body.URL == nil {
		writeAPIError(w, errBad("url is required"))
		return
	}
	raw := *body.URL
	u, err := s.img.ValidateURL(raw)
	if err != nil {
		writeAPIError(w, errBad(err.Error()))
		return
	}
	sum := sha256.Sum256([]byte(raw))
	ok, err := registered(sum[:])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "image is not registered for this paste")
		return
	}
	if !s.img.HostAllowed(u) {
		writeError(w, http.StatusNotFound, codeNotFound, "image host is not allowed on this server")
		return
	}

	res, perr := s.img.Get(r.Context(), raw)
	if perr != nil {
		if r.Context().Err() != nil {
			return // client gone
		}
		writeError(w, perr.Status, perr.Code, perr.Message)
		return
	}
	h := w.Header()
	h.Set("Content-Type", res.ContentType)
	h.Set("Content-Length", strconv.Itoa(len(res.Data)))
	h.Set("Cache-Control", "private, max-age=3600")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Data)
}
