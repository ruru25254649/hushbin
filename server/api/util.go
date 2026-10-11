package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"hushbin/server/store"
)

// API error codes (DESIGN.md §4).
const (
	codeInvalid      = "invalid"
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeLocked       = "locked"
	codeNotFound     = "not_found"
	codeConflict     = "conflict"
	codeTooLarge     = "too_large"
	codeRateLimited  = "rate_limited"
)

// Header names.
const (
	hdrReadAuth    = "X-Read-Auth"
	hdrOwnerToken  = "X-Owner-Token"
	hdrAuthorToken = "X-Author-Token"
)

const (
	tokenLen       = 32 // raw bytes of read auth / owner / author tokens
	saltLen        = 16
	verifierLen    = 32 // SHA-256
	minEnvelopeLen = 1 + 1 + 12 + 16
	envelopeV1     = 0x01
)

type apiError struct {
	status  int
	code    string
	msg     string
	version *int
}

func (e *apiError) Error() string { return e.code + ": " + e.msg }

func errBad(msg string) *apiError {
	return &apiError{status: http.StatusBadRequest, code: codeInvalid, msg: msg}
}

func errTooBig(msg string) *apiError {
	return &apiError{status: http.StatusRequestEntityTooLarge, code: codeTooLarge, msg: msg}
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Version *int   `json:"version,omitempty"`
}

func writeAPIError(w http.ResponseWriter, e *apiError) {
	writeJSON(w, e.status, errorBody{Error: e.code, Message: e.msg, Version: e.version})
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{Error: code, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"internal","message":"encoding failed"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
	_, _ = w.Write([]byte("\n"))
}

// b64 encodes as base64url without padding.
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

var strictB64 = base64.RawURLEncoding.Strict()

// unb64 decodes canonical base64url without padding. Unlike the standard
// decoder it rejects CR/LF and any character outside the URL alphabet.
func unb64(s string) ([]byte, bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return nil, false
		}
	}
	b, err := strictB64.DecodeString(s)
	if err != nil {
		return nil, false
	}
	return b, true
}

// encodedLen is the b64u length of n bytes.
func encodedLen(n int64) int64 { return (n*8 + 5) / 6 }

// parseToken decodes a 32-byte b64u token header value.
func parseToken(s string) ([]byte, bool) {
	if len(s) != 43 {
		return nil, false
	}
	b, ok := unb64(s)
	if !ok || len(b) != tokenLen {
		return nil, false
	}
	return b, true
}

// verifier returns SHA-256(raw).
func verifier(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}

// matches compares SHA-256(raw) with a stored verifier in constant time.
func matches(raw, stored []byte) bool {
	sum := sha256.Sum256(raw)
	return len(stored) == len(sum) && subtle.ConstantTimeCompare(sum[:], stored) == 1
}

// authorTag returns hex(SHA-256("tag:" || raw))[0:16].
func authorTag(raw []byte) string {
	h := sha256.New()
	h.Write([]byte("tag:"))
	h.Write(raw)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// decodeEnvelope decodes and structurally checks a b64u envelope field.
func decodeEnvelope(field string, s *string, max int64) ([]byte, *apiError) {
	if s == nil {
		return nil, errBad(field + " is required")
	}
	if int64(len(*s)) > encodedLen(max) {
		return nil, errTooBig(field + " is too large")
	}
	b, ok := unb64(*s)
	if !ok {
		return nil, errBad(field + " is not valid base64url")
	}
	if err := checkEnvelope(field, b, max); err != nil {
		return nil, err
	}
	return b, nil
}

func checkEnvelope(field string, b []byte, max int64) *apiError {
	if int64(len(b)) > max {
		return errTooBig(field + " is too large")
	}
	if len(b) < minEnvelopeLen || b[0] != envelopeV1 {
		return errBad(field + " is not a valid envelope")
	}
	return nil
}

// decodeJSON reads a JSON request body of at most limit bytes into dst.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) *apiError {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return bodyError(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		if err != nil {
			return bodyError(err)
		}
		return errBad("unexpected data after JSON body")
	}
	return nil
}

func bodyError(err error) *apiError {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return errTooBig("request body too large")
	}
	return errBad("malformed JSON body")
}

// ---------------------------------------------------------------------------
// Response shapes

type pasteHeaderJSON struct {
	ID         string          `json:"id"`
	Burn       bool            `json:"burn"`
	Discussion bool            `json:"discussion"`
	Salt       string          `json:"salt"`
	KDF        json.RawMessage `json:"kdf"`
	CreatedAt  int64           `json:"createdAt"`
	ExpiresAt  *int64          `json:"expiresAt"`
}

type openHeadJSON struct {
	ID         string `json:"id"`
	Burn       bool   `json:"burn"`
	Discussion bool   `json:"discussion"`
	Locked     bool   `json:"locked"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	ExpiresAt  *int64 `json:"expiresAt"`
	Version    int    `json:"version"`
	Content    string `json:"content"`
	// ImageTicket is set only when a burn paste with registered image refs
	// is opened; the key is always present.
	ImageTicket *string `json:"imageTicket"`
}

type commentJSON struct {
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

func toCommentJSON(c *store.Comment) commentJSON {
	out := commentJSON{
		ID: c.ID, ParentID: c.ParentID, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
		Author: c.Author, Tag: c.Tag, Deleted: c.Deleted, Version: c.Version,
	}
	if !c.Deleted && c.Content != nil {
		s := b64(c.Content)
		out.Content = &s
	}
	return out
}

type attachmentJSON struct {
	ID        string  `json:"id"`
	CommentID *string `json:"commentId"`
	Size      int64   `json:"size"`
	Meta      string  `json:"meta"`
}

func toAttachmentJSON(a *store.Attachment) attachmentJSON {
	return attachmentJSON{ID: a.ID, CommentID: a.CommentID, Size: a.Size, Meta: b64(a.Meta)}
}

type versionJSON struct {
	Version   int    `json:"version"`
	CreatedAt int64  `json:"createdAt"`
	Content   string `json:"content,omitempty"`
}

// Event payloads.
type evComment struct {
	Type    string      `json:"type"`
	Comment commentJSON `json:"comment"`
}

type evCommentDeleted struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type evAttachment struct {
	Type       string         `json:"type"`
	Attachment attachmentJSON `json:"attachment"`
}

type evPasteUpdated struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	UpdatedAt int64  `json:"updatedAt"`
}

type evPasteLocked struct {
	Type   string `json:"type"`
	Locked bool   `json:"locked"`
}

type evPasteDeleted struct {
	Type string `json:"type"`
}
