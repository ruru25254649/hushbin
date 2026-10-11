package api

import (
	"net/http"

	"hushbin/server/events"
	"hushbin/server/store"
)

type commentCreateBody struct {
	ID        string   `json:"id"`
	ParentID  *string  `json:"parentId"`
	Content   *string  `json:"content"`
	ImageRefs []string `json:"imageRefs"`
}

// POST /api/pastes/{id}/comments
func (s *Server) handleCommentCreate(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r, s.lim.comment) {
		return
	}
	p, ok := s.loadPaste(w, r)
	if !ok {
		return
	}
	// The discussion flag is public, so answer before checking read auth
	// (no password oracle on burn pastes).
	if !p.Discussion {
		writeError(w, http.StatusForbidden, codeLocked, "discussion is disabled")
		return
	}
	if !s.requireReadAuth(w, r, p) {
		return
	}
	isOwner, ownerRaw, ok := s.optionalOwner(w, r, p)
	if !ok {
		return
	}
	var raw []byte
	author := "guest"
	if isOwner {
		raw, author = ownerRaw, "owner"
	} else {
		if r.Header.Get(hdrAuthorToken) == "" {
			writeError(w, http.StatusForbidden, codeForbidden, "X-Owner-Token or X-Author-Token required")
			return
		}
		if raw = authorToken(r); raw == nil {
			writeError(w, http.StatusForbidden, codeForbidden, "malformed author token")
			return
		}
	}

	var body commentCreateBody
	if aerr := decodeJSON(w, r, encodedLen(s.cfg.MaxComment)+jsonSlack, &body); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if !store.ValidObjectID(body.ID) {
		writeAPIError(w, errBad("bad comment id"))
		return
	}
	if body.ParentID != nil && !store.ValidObjectID(*body.ParentID) {
		writeAPIError(w, errBad("bad parentId"))
		return
	}
	content, aerr := decodeEnvelope("content", body.Content, s.cfg.MaxComment)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	refs, aerr := parseImageRefs(body.ImageRefs)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	c, err := s.store.CreateComment(r.Context(), p.ID, store.NewComment{
		ID: body.ID, ParentID: body.ParentID, Author: author,
		Verifier: verifier(raw), Tag: authorTag(raw), Content: content, ImageRefs: refs,
	}, isOwner, s.cfg.MaxComments)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cj := toCommentJSON(c)
	s.hub.Publish(p.ID, events.CommentCreated, evComment{Type: events.CommentCreated, Comment: cj})
	writeJSON(w, http.StatusCreated, cj)
}

// commentAuthorizer returns the authorization callback for editing (or,
// with moderation, deleting) a comment.
//
//   - Owner comments: the paste owner token.
//   - Guest comments: the author token that created them.
//   - moderation: a valid owner token may act on any comment.
func commentAuthorizer(isOwner bool, authorRaw []byte, moderation bool) func(c *store.Comment) error {
	return func(c *store.Comment) error {
		if isOwner && (moderation || c.Author == "owner") {
			return nil
		}
		if c.Author == "guest" && authorRaw != nil && matches(authorRaw, c.Verifier) {
			return nil
		}
		return store.ErrForbidden
	}
}

// PUT /api/pastes/{id}/comments/{cid}
func (s *Server) handleCommentUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.allow(w, r, s.lim.comment) {
		return
	}
	p, ok := s.commentablePaste(w, r)
	if !ok {
		return
	}
	isOwner, _, ok := s.optionalOwner(w, r, p)
	if !ok {
		return
	}
	var body updateBody
	if aerr := decodeJSON(w, r, encodedLen(s.cfg.MaxComment)+jsonSlack, &body); aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	if body.BaseVersion == nil {
		writeAPIError(w, errBad("baseVersion is required"))
		return
	}
	content, aerr := decodeEnvelope("content", body.Content, s.cfg.MaxComment)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	refs, aerr := parseImageRefs(body.ImageRefs)
	if aerr != nil {
		writeAPIError(w, aerr)
		return
	}
	c, err := s.store.UpdateComment(r.Context(), p.ID, r.PathValue("cid"), content, *body.BaseVersion,
		s.cfg.MaxVersions, refs, commentAuthorizer(isOwner, authorToken(r), false))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	cj := toCommentJSON(c)
	s.hub.Publish(p.ID, events.CommentUpdated, evComment{Type: events.CommentUpdated, Comment: cj})
	writeJSON(w, http.StatusOK, cj)
}

// DELETE /api/pastes/{id}/comments/{cid}
func (s *Server) handleCommentDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.commentablePaste(w, r)
	if !ok {
		return
	}
	isOwner, _, ok := s.optionalOwner(w, r, p)
	if !ok {
		return
	}
	res, err := s.store.DeleteComment(r.Context(), p.ID, r.PathValue("cid"),
		commentAuthorizer(isOwner, authorToken(r), true))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if res.Tombstoned != nil {
		s.hub.Publish(p.ID, events.CommentUpdated, evComment{Type: events.CommentUpdated, Comment: toCommentJSON(res.Tombstoned)})
	}
	for _, id := range res.Removed {
		s.hub.Publish(p.ID, events.CommentDeleted, evCommentDeleted{Type: events.CommentDeleted, ID: id})
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /api/pastes/{id}/comments/{cid}/versions
func (s *Server) handleCommentVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.commentablePaste(w, r)
	if !ok {
		return
	}
	vs, err := s.store.CommentVersions(r.Context(), p.ID, r.PathValue("cid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]versionJSON, len(vs))
	for i, v := range vs {
		out[i] = versionJSON{Version: v.Version, CreatedAt: v.CreatedAt, Content: b64(v.Content)}
	}
	writeJSON(w, http.StatusOK, out)
}

// commentablePaste loads the paste for comment edit/delete/history. Pastes
// without a discussion (including burn pastes) have no comments; that is
// answered before read auth.
func (s *Server) commentablePaste(w http.ResponseWriter, r *http.Request) (*store.Paste, bool) {
	p, ok := s.loadPaste(w, r)
	if !ok {
		return nil, false
	}
	if !p.Discussion {
		writeError(w, http.StatusNotFound, codeNotFound, "comment not found")
		return nil, false
	}
	if !s.requireReadAuth(w, r, p) {
		return nil, false
	}
	return p, true
}
