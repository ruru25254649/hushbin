package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"hushbin/server/events"
	"hushbin/server/store"
)

type uploadMeta struct {
	ID        string  `json:"id"`
	CommentID *string `json:"commentId"`
	Meta      *string `json:"meta"`
}

// POST /api/pastes/{id}/attachments
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r, s.lim.upload) {
		return
	}
	p, ok := s.readableNonBurn(w, r)
	if !ok {
		return
	}
	isOwner, _, ok := s.optionalOwner(w, r, p)
	if !ok {
		return
	}
	cfg := &s.cfg
	metaLimit := encodedLen(cfg.MaxAttachmentMeta) + jsonSlack
	r.Body = http.MaxBytesReader(w, r.Body, metaLimit+cfg.MaxAttachment+4*partHeaderCost)
	mr, aerr := multipartReader(r)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	var metaRaw []byte
	var data *spooled
	defer func() {
		if data != nil {
			s.removeSpooled(map[string]*spooled{"": data})
		}
	}()
	for {
		part, err := nextPart(mr)
		if err == io.EOF {
			break
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
		switch name := part.FormName(); name {
		case "meta":
			if metaRaw != nil {
				writeAPIError(w, errBad("duplicate meta part"))
				return
			}
			if metaRaw, err = readPart(part, "meta", metaLimit); err != nil {
				s.fail(w, r, err)
				return
			}
		case "data":
			if data != nil {
				writeAPIError(w, errBad("duplicate data part"))
				return
			}
			if data, err = s.spool(part, "attachment", cfg.MaxAttachment); err != nil {
				s.fail(w, r, err)
				return
			}
		default:
			writeAPIError(w, errBad(fmt.Sprintf("unexpected part %q", name)))
			return
		}
		part.Close()
	}
	if metaRaw == nil || data == nil {
		writeAPIError(w, errBad("meta and data parts are required"))
		return
	}
	var m uploadMeta
	if err := json.Unmarshal(metaRaw, &m); err != nil {
		writeAPIError(w, errBad("malformed meta JSON"))
		return
	}
	if !store.ValidObjectID(m.ID) {
		writeAPIError(w, errBad("bad attachment id"))
		return
	}
	if m.CommentID != nil && !store.ValidObjectID(*m.CommentID) {
		writeAPIError(w, errBad("bad commentId"))
		return
	}
	meta, aerr := decodeEnvelope("meta", m.Meta, cfg.MaxAttachmentMeta)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if m.CommentID == nil && !isOwner {
		writeError(w, http.StatusForbidden, codeForbidden, "owner token required for paste attachments")
		return
	}
	a, err := s.store.CreateAttachment(r.Context(), p.ID, store.NewBlob{
		ID: m.ID, CommentID: m.CommentID, Meta: meta, TmpPath: data.path, Size: data.size,
	}, isOwner, store.AttachmentLimits{MaxTotal: cfg.MaxPasteAttachments, MaxCount: cfg.MaxAttachmentCount})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	aj := toAttachmentJSON(a)
	s.hub.Publish(p.ID, events.AttachmentCreated, evAttachment{Type: events.AttachmentCreated, Attachment: aj})
	writeJSON(w, http.StatusCreated, aj)
}

// GET /api/pastes/{id}/attachments/{aid}
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := s.readableNonBurn(w, r)
	if !ok {
		return
	}
	f, a, err := s.store.OpenAttachment(r.Context(), p.ID, r.PathValue("aid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(a.Size, 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.CopyN(w, f, a.Size); err != nil && r.Context().Err() == nil {
		s.log.Warn("attachment download interrupted", "err", err)
	}
}
