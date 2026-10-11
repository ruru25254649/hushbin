package api

import (
	"net/http"

	"hushbin/server/store"
)

// loadPaste loads the live paste named by the {id} path value, writing 404
// when it does not exist (malformed IDs, deleted and expired pastes alike).
func (s *Server) loadPaste(w http.ResponseWriter, r *http.Request) (*store.Paste, bool) {
	p, err := s.store.GetPaste(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	return p, true
}

// authGate refuses clients that have exhausted their failed-auth budget.
// Successful authentications do not consume the budget; failures do.
func (s *Server) authGate(w http.ResponseWriter, r *http.Request) bool {
	ok, wait := s.lim.auth.Check(info(r).key)
	if !ok {
		rateLimited(w, wait)
	}
	return ok
}

func (s *Server) authFailed(r *http.Request) { s.lim.auth.Take(info(r).key) }

// requireReadAuth checks X-Read-Auth against the paste's read verifier.
func (s *Server) requireReadAuth(w http.ResponseWriter, r *http.Request, p *store.Paste) bool {
	if !s.authGate(w, r) {
		return false
	}
	h := r.Header.Get(hdrReadAuth)
	if h == "" {
		writeError(w, http.StatusForbidden, codeUnauthorized, "read authorization required")
		return false
	}
	raw, ok := parseToken(h)
	if !ok || !matches(raw, p.ReadVerifier) {
		s.authFailed(r)
		writeError(w, http.StatusForbidden, codeUnauthorized, "wrong password or link")
		return false
	}
	return true
}

// requireOwner checks X-Owner-Token against the paste's owner verifier.
func (s *Server) requireOwner(w http.ResponseWriter, r *http.Request, p *store.Paste) bool {
	isOwner, _, ok := s.optionalOwner(w, r, p)
	if !ok {
		return false
	}
	if !isOwner {
		writeError(w, http.StatusForbidden, codeForbidden, "owner token required")
		return false
	}
	return true
}

// optionalOwner inspects X-Owner-Token. Absent: (false, nil, true). Present
// and valid: (true, raw, true). Present but wrong: writes 403 forbidden and
// returns ok=false.
func (s *Server) optionalOwner(w http.ResponseWriter, r *http.Request, p *store.Paste) (isOwner bool, raw []byte, ok bool) {
	h := r.Header.Get(hdrOwnerToken)
	if h == "" {
		return false, nil, true
	}
	if !s.authGate(w, r) {
		return false, nil, false
	}
	raw, valid := parseToken(h)
	if !valid || !matches(raw, p.OwnerVerifier) {
		s.authFailed(r)
		writeError(w, http.StatusForbidden, codeForbidden, "wrong owner token")
		return false, nil, false
	}
	return true, raw, true
}

// authorToken parses X-Author-Token (nil when absent or malformed).
func authorToken(r *http.Request) []byte {
	raw, ok := parseToken(r.Header.Get(hdrAuthorToken))
	if !ok {
		return nil
	}
	return raw
}
