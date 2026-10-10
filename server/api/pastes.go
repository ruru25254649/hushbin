package api

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"hushbin/server/events"
	"hushbin/server/store"
)

const (
	jsonSlack      = 64 << 10 // allowance for JSON framing around base64 payloads
	partHeaderCost = 1 << 10  // allowance per multipart part header
)

// ---------------------------------------------------------------------------
// Multipart helpers

type spooled struct {
	path string
	size int64
}

func (s *Server) removeSpooled(files map[string]*spooled) {
	for _, f := range files {
		_ = os.Remove(f.path)
	}
}

// spool streams a multipart part into a temporary file, enforcing max bytes
// and the envelope format.
func (s *Server) spool(part io.Reader, field string, max int64) (*spooled, error) {
	f, err := s.store.TempFile()
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	src := &trackingReader{r: io.LimitReader(part, max+1)}
	n, err := io.Copy(f, src)
	if src.err != nil {
		return nil, clientBodyError(src.err)
	}
	if err != nil {
		return nil, err // local I/O failure
	}
	if n > max {
		return nil, errTooBig(field + " is too large")
	}
	if n < minEnvelopeLen || src.first != envelopeV1 {
		return nil, errBad(field + " is not a valid envelope")
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	ok = true
	return &spooled{path: f.Name(), size: n}, nil
}

// trackingReader remembers the first byte read and any read error, so
// request-body errors (client side) can be told apart from local I/O errors.
type trackingReader struct {
	r     io.Reader
	first byte
	seen  bool
	err   error
}

func (t *trackingReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 && !t.seen {
		t.first, t.seen = p[0], true
	}
	if err != nil && err != io.EOF {
		t.err = err
	}
	return n, err
}

// clientBodyError maps an error reading the request body to 413 or 400.
func clientBodyError(err error) *apiError {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return errTooBig("request body too large")
	}
	return errBad("malformed multipart body")
}

func multipartReader(r *http.Request) (*multipart.Reader, *apiError) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" {
		return nil, errBad("expected multipart/form-data")
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, errBad("malformed multipart body")
	}
	return mr, nil
}

func nextPart(mr *multipart.Reader) (*multipart.Part, error) {
	p, err := mr.NextPart()
	if err == nil || err == io.EOF {
		return p, err
	}
	return nil, clientBodyError(err)
}

func readPart(part io.Reader, field string, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(part, max+1))
	if err != nil {
		return nil, clientBodyError(err)
	}
	if int64(len(b)) > max {
		return nil, errTooBig(field + " is too large")
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// POST /api/pastes

type createAttachment struct {
	ID   string  `json:"id"`
	Meta *string `json:"meta"`
}

type createMeta struct {
	Burn          *bool              `json:"burn"`
	Discussion    *bool              `json:"discussion"`
	ExpiresIn     *int64             `json:"expiresIn"`
	Salt          *string            `json:"salt"`
	KDF           json.RawMessage    `json:"kdf"`
	ReadVerifier  *string            `json:"readVerifier"`
	OwnerVerifier *string            `json:"ownerVerifier"`
	Content       *string            `json:"content"`
	Attachments   []createAttachment `json:"attachments"`
}

type kdfParams struct {
	Alg string `json:"alg"`
	M   int64  `json:"m"`
	T   int64  `json:"t"`
	P   int64  `json:"p"`
}

// parseKDF validates the kdf object and returns its canonical encoding, or
// nil for null/absent.
func parseKDF(raw json.RawMessage) ([]byte, *apiError) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errBad("kdf must be an object or null")
	}
	if len(fields) != 4 {
		return nil, errBad(`kdf must have exactly "alg", "m", "t" and "p"`)
	}
	for _, k := range []string{"alg", "m", "t", "p"} {
		if _, ok := fields[k]; !ok {
			return nil, errBad(`kdf must have exactly "alg", "m", "t" and "p"`)
		}
	}
	var k kdfParams
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, errBad("kdf has invalid field types")
	}
	if k.Alg != "argon2id" {
		return nil, errBad(`kdf.alg must be "argon2id"`)
	}
	if k.M < 8 || k.M > 4<<20 || k.T < 1 || k.T > 64 || k.P < 1 || k.P > 255 || k.M < 8*k.P {
		return nil, errBad("kdf parameters out of range")
	}
	b, _ := json.Marshal(k)
	return b, nil
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r, s.lim.create) {
		return
	}
	cfg := &s.cfg
	metaLimit := encodedLen(cfg.MaxBody) + int64(cfg.MaxAttachmentCount)*(encodedLen(cfg.MaxAttachmentMeta)+64) + jsonSlack
	total := metaLimit + cfg.MaxPasteAttachments + int64(cfg.MaxAttachmentCount+1)*partHeaderCost + jsonSlack
	r.Body = http.MaxBytesReader(w, r.Body, total)

	mr, aerr := multipartReader(r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	files := map[string]*spooled{}
	defer s.removeSpooled(files)
	var metaRaw []byte
	var spooledTotal int64
	for {
		part, err := nextPart(mr)
		if err == io.EOF {
			break
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		name := part.FormName()
		switch {
		case name == "meta":
			if metaRaw != nil {
				writeAPIError(w, errBad("duplicate meta part"))
				return
			}
			metaRaw, err = readPart(part, "meta", metaLimit)
			if err != nil {
				s.fail(w, r, err)
				return
			}
		case strings.HasPrefix(name, "att-"):
			id := strings.TrimPrefix(name, "att-")
			if !store.ValidObjectID(id) {
				writeAPIError(w, errBad("bad attachment part name"))
				return
			}
			if _, dup := files[id]; dup {
				writeError(w, http.StatusConflict, codeConflict, "duplicate attachment id")
				return
			}
			if len(files) >= cfg.MaxAttachmentCount {
				writeAPIError(w, errTooBig("too many attachments"))
				return
			}
			sp, err := s.spool(part, "attachment", cfg.MaxAttachment)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			files[id] = sp
			spooledTotal += sp.size
			if spooledTotal > cfg.MaxPasteAttachments {
				writeAPIError(w, errTooBig("attachments exceed the per-paste limit"))
				return
			}
		default:
			writeAPIError(w, errBad(fmt.Sprintf("unexpected part %q", name)))
			return
		}
		part.Close()
	}
	if metaRaw == nil {
		writeAPIError(w, errBad("missing meta part"))
		return
	}

	var m createMeta
	if err := json.Unmarshal(metaRaw, &m); err != nil {
		writeAPIError(w, errBad("malformed meta JSON"))
		return
	}
	np, aerr := s.validateCreate(&m)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	blobs := make([]store.NewBlob, 0, len(m.Attachments))
	seen := map[string]bool{}
	for _, a := range m.Attachments {
		if !store.ValidObjectID(a.ID) {
			writeAPIError(w, errBad("bad attachment id"))
			return
		}
		if seen[a.ID] {
			writeError(w, http.StatusConflict, codeConflict, "duplicate attachment id")
			return
		}
		seen[a.ID] = true
		meta, aerr := decodeEnvelope("attachment meta", a.Meta, cfg.MaxAttachmentMeta)
		if aerr != nil {
			writeAPIError(w, aerr)
			return
		}
		sp, ok := files[a.ID]
		if !ok {
			writeAPIError(w, errBad("missing data part for attachment "+a.ID))
			return
		}
		blobs = append(blobs, store.NewBlob{ID: a.ID, Meta: meta, TmpPath: sp.path, Size: sp.size})
	}
	if len(files) != len(blobs) {
		writeAPIError(w, errBad("attachment part without matching meta entry"))
		return
	}
	if len(blobs) > cfg.MaxAttachmentCount {
		writeAPIError(w, errTooBig("too many attachments"))
		return
	}

	p, err := s.store.CreatePaste(r.Context(), *np, blobs)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		ID        string `json:"id"`
		ExpiresAt *int64 `json:"expiresAt"`
	}{p.ID, p.ExpiresAt})
}

func (s *Server) validateCreate(m *createMeta) (*store.NewPaste, *apiError) {
	if m.Burn == nil || m.Discussion == nil {
		return nil, errBad("burn and discussion are required")
	}
	if *m.Burn && *m.Discussion {
		return nil, errBad("burn pastes cannot have a discussion")
	}
	if m.ExpiresIn == nil || !s.cfg.ExpiryAllowed(*m.ExpiresIn) {
		return nil, errBad("expiresIn must be one of the allowed values")
	}
	if m.Salt == nil {
		return nil, errBad("salt is required")
	}
	salt, ok := unb64(*m.Salt)
	if !ok || len(salt) != saltLen {
		return nil, errBad("salt must be 16 bytes")
	}
	kdf, aerr := parseKDF(m.KDF)
	if aerr != nil {
		return nil, aerr
	}
	var rv, ov []byte
	for _, f := range []struct {
		name string
		in   *string
		out  *[]byte
	}{{"readVerifier", m.ReadVerifier, &rv}, {"ownerVerifier", m.OwnerVerifier, &ov}} {
		if f.in == nil {
			return nil, errBad(f.name + " is required")
		}
		b, ok := unb64(*f.in)
		if !ok || len(b) != verifierLen {
			return nil, errBad(f.name + " must be a 32-byte SHA-256 digest")
		}
		*f.out = b
	}
	content, aerr := decodeEnvelope("content", m.Content, s.cfg.MaxBody)
	if aerr != nil {
		return nil, aerr
	}
	return &store.NewPaste{
		Burn: *m.Burn, Discussion: *m.Discussion, ExpiresIn: *m.ExpiresIn,
		Salt: salt, KDF: kdf, ReadVerifier: rv, OwnerVerifier: ov, Content: content,
	}, nil
}

// ---------------------------------------------------------------------------
// GET /api/pastes/{id}

func (s *Server) handleHeader(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPaste(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, pasteHeaderJSON{
		ID: p.ID, Burn: p.Burn, Discussion: p.Discussion, Salt: b64(p.Salt),
		KDF: json.RawMessage(p.KDF), CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
	})
}

// ---------------------------------------------------------------------------
// POST /api/pastes/{id}/open

func openHead(p *store.Paste, content []byte) []byte {
	b, _ := json.Marshal(openHeadJSON{
		ID: p.ID, Burn: p.Burn, Discussion: p.Discussion, Locked: p.Locked,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ExpiresAt: p.ExpiresAt,
		Version: p.Version, Content: b64(content),
	})
	return b[:len(b)-1] // drop '}' to append the arrays
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPaste(w, r)
	if !ok {
		return
	}
	if !s.requireReadAuth(w, r, p) {
		return
	}
	if p.Burn {
		s.openBurn(w, r, p.ID)
		return
	}
	// The response is streamed from a read snapshot so that comment
	// contents are never all held in memory at once.
	wrote := false
	err := s.store.View(r.Context(), func(v *store.View) error {
		p, err := v.Paste(p.ID)
		if err != nil {
			return err
		}
		content, err := v.Content(p.ID, p.Version)
		if err != nil {
			return err
		}
		atts, err := v.Attachments(p.ID)
		if err != nil {
			return err
		}
		aj := make([]attachmentJSON, len(atts))
		for i := range atts {
			aj[i] = toAttachmentJSON(&atts[i])
		}
		attsJSON, err := json.Marshal(aj)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		wrote = true
		bw := bufio.NewWriterSize(w, 32<<10)
		bw.Write(openHead(p, content))
		bw.WriteString(`,"attachments":`)
		bw.Write(attsJSON)
		bw.WriteString(`,"comments":[`)
		first := true
		err = v.Comments(p.ID, func(c *store.Comment) error {
			if !first {
				bw.WriteByte(',')
			}
			first = false
			b, err := json.Marshal(toCommentJSON(c))
			if err != nil {
				return err
			}
			_, err = bw.Write(b)
			return err
		})
		if err != nil {
			return err
		}
		bw.WriteString("]}\n")
		return bw.Flush()
	})
	if err != nil {
		if !wrote {
			s.fail(w, r, err)
			return
		}
		if r.Context().Err() == nil {
			s.log.Error("open: response aborted", "err", err)
		}
		panic(http.ErrAbortHandler)
	}
}

// openBurn atomically deletes a burn paste and streams it, attachment data
// inlined, to the single winning caller.
func (s *Server) openBurn(w http.ResponseWriter, r *http.Request, id string) {
	b, err := s.store.Burn(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer b.Cleanup()
	s.hub.Publish(id, events.PasteDeleted, evPasteDeleted{Type: events.PasteDeleted})

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	bw := bufio.NewWriterSize(w, 64<<10)
	bw.Write(openHead(b.Paste, b.Content))
	bw.WriteString(`,"attachments":[`)
	for i := range b.Attachments {
		a := &b.Attachments[i]
		_ = rc.SetWriteDeadline(time.Now().Add(requestDeadline))
		if i > 0 {
			bw.WriteByte(',')
		}
		aj, _ := json.Marshal(toAttachmentJSON(a))
		bw.Write(aj[:len(aj)-1])
		bw.WriteString(`,"data":"`)
		if err := streamBase64(bw, b.BlobPath(a.ID)); err != nil {
			if r.Context().Err() == nil {
				s.log.Error("burn open: streaming attachment failed", "err", err)
			}
			panic(http.ErrAbortHandler)
		}
		bw.WriteString(`"}`)
	}
	bw.WriteString(`],"comments":[]}` + "\n")
	if err := bw.Flush(); err != nil {
		panic(http.ErrAbortHandler)
	}
}

func streamBase64(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := base64.NewEncoder(base64.RawURLEncoding, w)
	if _, err := io.Copy(enc, f); err != nil {
		return err
	}
	return enc.Close()
}

// ---------------------------------------------------------------------------
// Owner endpoints

type updateBody struct {
	Content     *string `json:"content"`
	BaseVersion *int    `json:"baseVersion"`
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPaste(w, r)
	if !ok || !s.requireOwner(w, r, p) {
		return
	}
	var body updateBody
	if aerr := decodeJSON(w, r, encodedLen(s.cfg.MaxBody)+jsonSlack, &body); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if body.BaseVersion == nil {
		writeAPIError(w, errBad("baseVersion is required"))
		return
	}
	content, aerr := decodeEnvelope("content", body.Content, s.cfg.MaxBody)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	up, err := s.store.UpdatePaste(r.Context(), p.ID, content, *body.BaseVersion, s.cfg.MaxVersions)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.hub.Publish(p.ID, events.PasteUpdated, evPasteUpdated{Type: events.PasteUpdated, Version: up.Version, UpdatedAt: up.UpdatedAt})
	writeJSON(w, http.StatusOK, struct {
		Version   int   `json:"version"`
		UpdatedAt int64 `json:"updatedAt"`
	}{up.Version, up.UpdatedAt})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPaste(w, r)
	if !ok || !s.requireOwner(w, r, p) {
		return
	}
	if err := s.store.DeletePaste(r.Context(), p.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.hub.Publish(p.ID, events.PasteDeleted, evPasteDeleted{Type: events.PasteDeleted})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPaste(w, r)
	if !ok || !s.requireOwner(w, r, p) {
		return
	}
	var body struct {
		Locked *bool `json:"locked"`
	}
	if aerr := decodeJSON(w, r, 4<<10, &body); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if body.Locked == nil {
		writeAPIError(w, errBad("locked is required"))
		return
	}
	if err := s.store.SetLocked(r.Context(), p.ID, *body.Locked); err != nil {
		s.fail(w, r, err)
		return
	}
	s.hub.Publish(p.ID, events.PasteLocked, evPasteLocked{Type: events.PasteLocked, Locked: *body.Locked})
	writeJSON(w, http.StatusOK, struct {
		Locked bool `json:"locked"`
	}{*body.Locked})
}

// ---------------------------------------------------------------------------
// Versions

// readableNonBurn loads the paste and checks read auth for endpoints that
// must not expose burn pastes (they would bypass burn-on-open). The burn
// check precedes auth so these endpoints are no password oracle.
func (s *Server) readableNonBurn(w http.ResponseWriter, r *http.Request) (*store.Paste, bool) {
	p, ok := s.loadPaste(w, r)
	if !ok {
		return nil, false
	}
	if p.Burn {
		writeAPIError(w, errBad("not available for burn-after-reading pastes"))
		return nil, false
	}
	if !s.requireReadAuth(w, r, p) {
		return nil, false
	}
	return p, true
}

func (s *Server) handleVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.readableNonBurn(w, r)
	if !ok {
		return
	}
	vs, err := s.store.PasteVersions(r.Context(), p.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]versionJSON, len(vs))
	for i, v := range vs {
		out[i] = versionJSON{Version: v.Version, CreatedAt: v.CreatedAt}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.readableNonBurn(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("v"))
	if err != nil || n < 1 || strconv.Itoa(n) != r.PathValue("v") {
		writeError(w, http.StatusNotFound, codeNotFound, "version not found")
		return
	}
	v, err := s.store.PasteVersion(r.Context(), p.ID, n)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, versionJSON{Version: v.Version, CreatedAt: v.CreatedAt, Content: b64(v.Content)})
}
