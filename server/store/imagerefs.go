package store

import (
	"context"
	"database/sql"
	"errors"
)

// ImageRefLen is the size of an image ref: SHA-256 of the image URL.
const ImageRefLen = 32

// addImageRefs unions refs into the paste's image-ref set inside tx. If the
// set would exceed the per-paste cap it returns too_large and the caller's
// transaction rolls back, so nothing of the request is written.
func (s *Store) addImageRefs(ctx context.Context, tx *sql.Tx, pasteID string, refs [][]byte) error {
	if len(refs) == 0 {
		return nil
	}
	for _, r := range refs {
		if len(r) != ImageRefLen {
			return errInvalid("image refs must be 32 bytes")
		}
	}
	for _, r := range refs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO image_refs (paste_id, hash) VALUES (?, ?)`,
			pasteID, r); err != nil {
			return err
		}
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_refs WHERE paste_id = ?`, pasteID).Scan(&n); err != nil {
		return err
	}
	if n > s.maxImageRefs {
		return errTooLarge("image reference limit reached")
	}
	return nil
}

func listImageRefs(ctx context.Context, q querier, pasteID string) ([][]byte, error) {
	rows, err := q.QueryContext(ctx, `SELECT hash FROM image_refs WHERE paste_id = ?`, pasteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// AddImageRefs registers refs for a live paste (POST /image-refs).
// authorize runs inside the transaction with the current paste and decides
// whether the caller may register refs (lock/discussion state is read in
// the same transaction, so it cannot change underneath).
func (s *Store) AddImageRefs(ctx context.Context, pasteID string, refs [][]byte, authorize func(p *Paste) error) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		p, err := s.getPaste(ctx, tx, pasteID)
		if err != nil {
			return err
		}
		if p.Burn {
			return errInvalid("burn pastes cannot register image refs")
		}
		if err := authorize(p); err != nil {
			return err
		}
		return s.addImageRefs(ctx, tx, pasteID, refs)
	})
}

// HasImageRef reports whether ref is registered for the live paste.
func (s *Store) HasImageRef(ctx context.Context, pasteID string, ref []byte) (bool, error) {
	if !ValidPasteID(pasteID) || len(ref) != ImageRefLen {
		return false, nil
	}
	var one int
	err := s.r.QueryRowContext(ctx, `SELECT 1 FROM image_refs r JOIN pastes p ON p.id = r.paste_id
		WHERE r.paste_id = ? AND r.hash = ? AND (p.expires_at IS NULL OR p.expires_at > ?)`,
		pasteID, ref, s.NowMs()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ImageRefCount returns the number of refs registered for a paste.
func (s *Store) ImageRefCount(ctx context.Context, pasteID string) (int, error) {
	var n int
	err := s.r.QueryRowContext(ctx, `SELECT COUNT(*) FROM image_refs WHERE paste_id = ?`, pasteID).Scan(&n)
	return n, err
}

// ErrLocked returns the error authorize callbacks use for a locked or
// disabled discussion.
func ErrLocked(msg string) error { return errLocked(msg) }
