// Package store persists pastes, versions, comments and attachments in
// SQLite (WAL mode) and attachment blobs on disk at
// $HB_DATA_DIR/att/<pasteId>/<attId>. Everything stored is opaque
// ciphertext; the store only enforces structural rules (existence, expiry,
// locking, version and size caps, uniqueness).
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Error codes, mirroring the API error codes of DESIGN.md §4.
const (
	CodeInvalid   = "invalid"
	CodeForbidden = "forbidden"
	CodeLocked    = "locked"
	CodeNotFound  = "not_found"
	CodeConflict  = "conflict"
	CodeTooLarge  = "too_large"
)

// Error is a rule violation reported by the store.
type Error struct {
	Code    string
	Message string
	// Version is the current version for stale-baseVersion conflicts.
	Version    int
	HasVersion bool
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errNotFound(msg string) error { return &Error{Code: CodeNotFound, Message: msg} }
func errInvalid(msg string) error  { return &Error{Code: CodeInvalid, Message: msg} }
func errLocked(msg string) error   { return &Error{Code: CodeLocked, Message: msg} }
func errTooLarge(msg string) error { return &Error{Code: CodeTooLarge, Message: msg} }
func errDuplicate() error          { return &Error{Code: CodeConflict, Message: "duplicate id"} }
func errStale(v int) error {
	return &Error{Code: CodeConflict, Message: "stale baseVersion", Version: v, HasVersion: true}
}

// ErrForbidden may be returned by authorize callbacks.
var ErrForbidden = &Error{Code: CodeForbidden, Message: "not allowed"}

// IsCode reports whether err is a store *Error with the given code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

var (
	pasteIDRe  = regexp.MustCompile(`^[A-Za-z0-9]{10}$`)
	objectIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)
)

// ValidPasteID reports whether s is a well-formed paste ID.
func ValidPasteID(s string) bool { return pasteIDRe.MatchString(s) }

// ValidObjectID reports whether s is a well-formed comment/attachment ID.
func ValidObjectID(s string) bool { return objectIDRe.MatchString(s) }

const pasteIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// NewPasteID returns a random 10-character [A-Za-z0-9] ID.
func NewPasteID() string {
	out := make([]byte, 0, 10)
	var buf [16]byte
	for len(out) < 10 {
		if _, err := rand.Read(buf[:]); err != nil {
			panic(err)
		}
		for _, b := range buf {
			if b < 248 { // 248 = 4*62, unbiased
				out = append(out, pasteIDAlphabet[int(b)%62])
				if len(out) == 10 {
					break
				}
			}
		}
	}
	return string(out)
}

// Paste is a stored paste.
type Paste struct {
	ID            string
	Burn          bool
	Discussion    bool
	Locked        bool
	Salt          []byte
	KDF           []byte // canonical JSON object, nil when no password
	ReadVerifier  []byte
	OwnerVerifier []byte
	CreatedAt     int64
	UpdatedAt     int64
	ExpiresAt     *int64
	Version       int
}

// Comment is a stored comment. Content is nil for tombstones.
type Comment struct {
	ID        string
	ParentID  *string
	Author    string // "owner" | "guest"
	Verifier  []byte // SHA-256 of the author's raw token
	Tag       string
	CreatedAt int64
	UpdatedAt int64
	Version   int
	Deleted   bool
	Content   []byte
}

// Attachment is a stored attachment's metadata.
type Attachment struct {
	ID        string
	CommentID *string
	Size      int64
	Meta      []byte
	CreatedAt int64
}

// Version is one entry of an object's history.
type Version struct {
	Version   int
	CreatedAt int64
	Content   []byte
}

// Store is the persistence layer.
type Store struct {
	w       *sql.DB // single connection, BEGIN IMMEDIATE: serialises writers
	r       *sql.DB // readers (query_only)
	dataDir string
	attDir  string
	tmpDir  string
	now     func() time.Time

	maxImageRefs int
}

// Options configure Open.
type Options struct {
	// Now returns the current time; defaults to time.Now. Tests use it to
	// control expiry.
	Now func() time.Time
	// MaxReaders bounds the reader connection pool (default 8).
	MaxReaders int
	// MaxImageRefs caps the registered image references per paste
	// (HB_MAX_IMAGE_REFS, default 200).
	MaxImageRefs int
}

const schemaVersion = 2

const schema = `
CREATE TABLE IF NOT EXISTS pastes (
	id             TEXT    PRIMARY KEY,
	burn           INTEGER NOT NULL,
	discussion     INTEGER NOT NULL,
	locked         INTEGER NOT NULL DEFAULT 0,
	salt           BLOB    NOT NULL,
	kdf            TEXT,
	read_verifier  BLOB    NOT NULL,
	owner_verifier BLOB    NOT NULL,
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL,
	expires_at     INTEGER,
	version        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS pastes_expires_at ON pastes(expires_at) WHERE expires_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS paste_versions (
	paste_id   TEXT    NOT NULL REFERENCES pastes(id) ON DELETE CASCADE,
	version    INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	content    BLOB    NOT NULL,
	PRIMARY KEY (paste_id, version)
);

CREATE TABLE IF NOT EXISTS comments (
	paste_id        TEXT    NOT NULL REFERENCES pastes(id) ON DELETE CASCADE,
	id              TEXT    NOT NULL,
	parent_id       TEXT,
	author          TEXT    NOT NULL CHECK (author IN ('owner', 'guest')),
	author_verifier BLOB    NOT NULL,
	tag             TEXT    NOT NULL,
	created_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL,
	version         INTEGER NOT NULL,
	deleted         INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (paste_id, id)
);
CREATE INDEX IF NOT EXISTS comments_parent ON comments(paste_id, parent_id);

CREATE TABLE IF NOT EXISTS comment_versions (
	paste_id   TEXT    NOT NULL,
	comment_id TEXT    NOT NULL,
	version    INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	content    BLOB    NOT NULL,
	PRIMARY KEY (paste_id, comment_id, version),
	FOREIGN KEY (paste_id, comment_id) REFERENCES comments(paste_id, id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS attachments (
	paste_id   TEXT    NOT NULL REFERENCES pastes(id) ON DELETE CASCADE,
	id         TEXT    NOT NULL,
	comment_id TEXT,
	size       INTEGER NOT NULL,
	meta       BLOB    NOT NULL,
	created_at INTEGER NOT NULL,
	PRIMARY KEY (paste_id, id)
);
CREATE INDEX IF NOT EXISTS attachments_comment ON attachments(paste_id, comment_id);

-- v2: SHA-256 hashes of remote image URLs the image proxy may fetch for a
-- paste (the URLs themselves are never stored).
CREATE TABLE IF NOT EXISTS image_refs (
	paste_id TEXT NOT NULL REFERENCES pastes(id) ON DELETE CASCADE,
	hash     BLOB NOT NULL,
	PRIMARY KEY (paste_id, hash)
) WITHOUT ROWID;
`

// Open opens (creating if needed) the database and blob directories under
// dataDir.
func Open(dataDir string, opts Options) (*Store, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxReaders <= 0 {
		opts.MaxReaders = 8
	}
	if opts.MaxImageRefs <= 0 {
		opts.MaxImageRefs = 200
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if strings.ContainsRune(abs, '?') {
		return nil, fmt.Errorf("data dir %q must not contain '?'", abs)
	}
	s := &Store{
		dataDir: abs,
		attDir:  filepath.Join(abs, "att"),
		tmpDir:  filepath.Join(abs, "tmp"),
		now:     opts.Now,

		maxImageRefs: opts.MaxImageRefs,
	}
	for _, d := range []string{abs, s.attDir, s.tmpDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	dbPath := filepath.Join(abs, "hushbin.db")
	// Pre-create the database owner-only; SQLite gives the -wal and -shm
	// files the same permissions as the main file.
	if f, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0o600); err != nil {
		return nil, err
	} else {
		f.Close()
	}
	common := "_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)"

	s.w, err = sql.Open("sqlite", dbPath+"?"+common+"&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	s.w.SetMaxOpenConns(1)
	s.w.SetMaxIdleConns(1)
	s.w.SetConnMaxLifetime(0)
	s.w.SetConnMaxIdleTime(0)
	if err := s.migrate(); err != nil {
		s.w.Close()
		return nil, err
	}

	s.r, err = sql.Open("sqlite", dbPath+"?"+common+"&_pragma=query_only(1)")
	if err != nil {
		s.w.Close()
		return nil, err
	}
	s.r.SetMaxOpenConns(opts.MaxReaders)
	s.r.SetMaxIdleConns(opts.MaxReaders)
	if err := s.r.Ping(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	var mode string
	if err := s.w.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("sqlite: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("sqlite: could not enable WAL (journal_mode=%s)", mode)
	}
	var v int
	if err := s.w.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", v, schemaVersion)
	}
	if _, err := s.w.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	_, err := s.w.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	return err
}

// Close closes the database.
func (s *Store) Close() error {
	var errs []error
	if s.r != nil {
		errs = append(errs, s.r.Close())
	}
	if s.w != nil {
		errs = append(errs, s.w.Close())
	}
	return errors.Join(errs...)
}

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.r.PingContext(ctx) }

// DataDir returns the absolute data directory.
func (s *Store) DataDir() string { return s.dataDir }

// NowMs returns the store's current time in Unix milliseconds.
func (s *Store) NowMs() int64 { return s.now().UnixMilli() }

// querier is satisfied by *sql.DB and *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// write runs fn in an IMMEDIATE transaction on the single writer
// connection. The transaction is detached from ctx cancellation so a client
// disconnect cannot interrupt it half-way (writes are short).
func (s *Store) write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	ctx = context.WithoutCancel(ctx)
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

const pasteCols = `id, burn, discussion, locked, salt, kdf, read_verifier, owner_verifier,
	created_at, updated_at, expires_at, version`

const aliveCond = `(expires_at IS NULL OR expires_at > ?)`

func scanPaste(row interface{ Scan(...any) error }) (*Paste, error) {
	var p Paste
	var kdf sql.NullString
	var exp sql.NullInt64
	if err := row.Scan(&p.ID, &p.Burn, &p.Discussion, &p.Locked, &p.Salt, &kdf,
		&p.ReadVerifier, &p.OwnerVerifier, &p.CreatedAt, &p.UpdatedAt, &exp, &p.Version); err != nil {
		return nil, err
	}
	if kdf.Valid {
		p.KDF = []byte(kdf.String)
	}
	if exp.Valid {
		v := exp.Int64
		p.ExpiresAt = &v
	}
	return &p, nil
}

func (s *Store) getPaste(ctx context.Context, q querier, id string) (*Paste, error) {
	if !ValidPasteID(id) {
		return nil, errNotFound("paste not found")
	}
	p, err := scanPaste(q.QueryRowContext(ctx,
		`SELECT `+pasteCols+` FROM pastes WHERE id = ? AND `+aliveCond, id, s.NowMs()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound("paste not found")
	}
	return p, err
}

// GetPaste returns a live (unexpired) paste.
func (s *Store) GetPaste(ctx context.Context, id string) (*Paste, error) {
	return s.getPaste(ctx, s.r, id)
}

// ---------------------------------------------------------------------------
// Blobs

func (s *Store) pasteDir(pasteID string) string { return filepath.Join(s.attDir, pasteID) }

func (s *Store) blobPath(pasteID, attID string) (string, error) {
	if !ValidPasteID(pasteID) || !ValidObjectID(attID) {
		return "", errInvalid("bad id")
	}
	return filepath.Join(s.attDir, pasteID, attID), nil
}

// TempFile creates a temporary file on the same filesystem as the blob
// directory, for streaming uploads before they are committed.
func (s *Store) TempFile() (*os.File, error) {
	return os.CreateTemp(s.tmpDir, "upload-*")
}

func (s *Store) removePasteDir(pasteID string) error {
	if !ValidPasteID(pasteID) {
		return nil
	}
	return os.RemoveAll(s.pasteDir(pasteID))
}

// CleanTemp removes temporary files older than maxAge (left behind by
// crashes or burned pastes whose response was interrupted).
func (s *Store) CleanTemp(maxAge time.Duration) error {
	entries, err := os.ReadDir(s.tmpDir)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.tmpDir, e.Name()))
	}
	return nil
}

// RemoveOrphanBlobs deletes blob directories whose paste no longer exists
// (e.g. after a crash between commit and blob removal). Call it before
// serving requests.
func (s *Store) RemoveOrphanBlobs(ctx context.Context) (int, error) {
	entries, err := os.ReadDir(s.attDir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		var one int
		err := s.r.QueryRowContext(ctx, `SELECT 1 FROM pastes WHERE id = ?`, name).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			if err := os.RemoveAll(filepath.Join(s.attDir, name)); err == nil {
				n++
			}
			continue
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Pastes

// NewPaste is the input to CreatePaste.
type NewPaste struct {
	Burn          bool
	Discussion    bool
	ExpiresIn     int64 // seconds, 0 = never
	Salt          []byte
	KDF           []byte // canonical JSON or nil
	ReadVerifier  []byte
	OwnerVerifier []byte
	Content       []byte
	ImageRefs     [][]byte // SHA-256 hashes of remote image URLs
}

// NewBlob is an uploaded attachment waiting in a temporary file.
type NewBlob struct {
	ID        string
	CommentID *string
	Meta      []byte
	TmpPath   string
	Size      int64
}

// CreatePaste stores a new paste and moves its attachment blobs into place.
// The caller must have validated sizes and limits.
func (s *Store) CreatePaste(ctx context.Context, np NewPaste, blobs []NewBlob) (*Paste, error) {
	for _, b := range blobs {
		if !ValidObjectID(b.ID) {
			return nil, errInvalid("bad attachment id")
		}
	}
	errTaken := errors.New("id taken")
	for attempt := 0; attempt < 8; attempt++ {
		now := s.NowMs()
		p := &Paste{
			ID: NewPasteID(), Burn: np.Burn, Discussion: np.Discussion,
			Salt: np.Salt, KDF: np.KDF, ReadVerifier: np.ReadVerifier, OwnerVerifier: np.OwnerVerifier,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		if np.ExpiresIn > 0 {
			e := now + np.ExpiresIn*1000
			p.ExpiresAt = &e
		}
		var kdf any
		if p.KDF != nil {
			kdf = string(p.KDF)
		}
		var exp any
		if p.ExpiresAt != nil {
			exp = *p.ExpiresAt
		}
		madeDir := false
		err := s.write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `INSERT INTO pastes (`+pasteCols+`)
				VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, 1) ON CONFLICT(id) DO NOTHING`,
				p.ID, p.Burn, p.Discussion, p.Salt, kdf, p.ReadVerifier, p.OwnerVerifier,
				p.CreatedAt, p.UpdatedAt, exp)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errTaken
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO paste_versions (paste_id, version, created_at, content)
				VALUES (?, 1, ?, ?)`, p.ID, now, np.Content); err != nil {
				return err
			}
			if err := s.addImageRefs(ctx, tx, p.ID, np.ImageRefs); err != nil {
				return err
			}
			if len(blobs) == 0 {
				return nil
			}
			dir := s.pasteDir(p.ID)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			madeDir = true
			for _, b := range blobs {
				if _, err := tx.ExecContext(ctx, `INSERT INTO attachments (paste_id, id, comment_id, size, meta, created_at)
					VALUES (?, ?, NULL, ?, ?, ?)`, p.ID, b.ID, b.Size, b.Meta, now); err != nil {
					return err
				}
				if err := os.Rename(b.TmpPath, filepath.Join(dir, b.ID)); err != nil {
					return err
				}
			}
			return nil
		})
		if errors.Is(err, errTaken) {
			continue
		}
		if err != nil {
			if madeDir {
				_ = s.removePasteDir(p.ID)
			}
			return nil, err
		}
		return p, nil
	}
	return nil, errors.New("could not allocate a paste id")
}

// View is a consistent read-only snapshot.
type View struct {
	s   *Store
	ctx context.Context
	tx  *sql.Tx
}

// View runs fn inside a read transaction (a single consistent snapshot).
func (s *Store) View(ctx context.Context, fn func(v *View) error) error {
	tx, err := s.r.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(&View{s: s, ctx: ctx, tx: tx})
}

// Paste returns a live paste.
func (v *View) Paste(id string) (*Paste, error) { return v.s.getPaste(v.ctx, v.tx, id) }

// Content returns the content of the given paste version.
func (v *View) Content(pasteID string, version int) ([]byte, error) {
	var c []byte
	err := v.tx.QueryRowContext(v.ctx, `SELECT content FROM paste_versions WHERE paste_id = ? AND version = ?`,
		pasteID, version).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound("version not found")
	}
	return c, err
}

// Attachments lists a paste's attachments in upload order.
func (v *View) Attachments(pasteID string) ([]Attachment, error) {
	return listAttachments(v.ctx, v.tx, pasteID)
}

func listAttachments(ctx context.Context, q querier, pasteID string) ([]Attachment, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, comment_id, size, meta, created_at FROM attachments
		WHERE paste_id = ? ORDER BY created_at, rowid`, pasteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		var cid sql.NullString
		if err := rows.Scan(&a.ID, &cid, &a.Size, &a.Meta, &a.CreatedAt); err != nil {
			return nil, err
		}
		if cid.Valid {
			s := cid.String
			a.CommentID = &s
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const commentCols = `c.id, c.parent_id, c.author, c.author_verifier, c.tag, c.created_at, c.updated_at,
	c.version, c.deleted, cv.content`

const commentFrom = `FROM comments c LEFT JOIN comment_versions cv
	ON cv.paste_id = c.paste_id AND cv.comment_id = c.id AND cv.version = c.version`

func scanComment(row interface{ Scan(...any) error }) (*Comment, error) {
	var c Comment
	var parent sql.NullString
	if err := row.Scan(&c.ID, &parent, &c.Author, &c.Verifier, &c.Tag, &c.CreatedAt, &c.UpdatedAt,
		&c.Version, &c.Deleted, &c.Content); err != nil {
		return nil, err
	}
	if parent.Valid {
		p := parent.String
		c.ParentID = &p
	}
	if c.Deleted {
		c.Content = nil
	}
	return &c, nil
}

// Comments calls fn for every comment of the paste in creation order,
// streaming rows (only one comment's content is held at a time).
func (v *View) Comments(pasteID string, fn func(c *Comment) error) error {
	rows, err := v.tx.QueryContext(v.ctx, `SELECT `+commentCols+` `+commentFrom+`
		WHERE c.paste_id = ? ORDER BY c.created_at, c.rowid`, pasteID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return err
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Burned is the result of opening a burn-after-reading paste. The rows are
// already gone; blobs live in Dir until the caller calls Cleanup.
type Burned struct {
	Paste       *Paste
	Content     []byte
	Attachments []Attachment
	ImageRefs   [][]byte // the paste's registered image refs at burn time
	dir         string
}

// BlobPath returns the path of an attachment blob of a burned paste.
func (b *Burned) BlobPath(attID string) string { return filepath.Join(b.dir, attID) }

// Cleanup removes the burned paste's blobs.
func (b *Burned) Cleanup() {
	if b.dir != "" {
		_ = os.RemoveAll(b.dir)
	}
}

// Burn atomically reads and deletes a burn-after-reading paste. Exactly one
// concurrent caller succeeds; the others get not_found. Attachment blobs are
// moved out of the live tree inside the transaction so the caller can stream
// them; the caller must call Cleanup.
func (s *Store) Burn(ctx context.Context, id string) (*Burned, error) {
	var out *Burned
	err := s.write(ctx, func(tx *sql.Tx) error {
		p, err := s.getPaste(ctx, tx, id)
		if err != nil {
			return err
		}
		if !p.Burn {
			return errInvalid("not a burn paste")
		}
		var content []byte
		if err := tx.QueryRowContext(ctx, `SELECT content FROM paste_versions WHERE paste_id = ? AND version = ?`,
			id, p.Version).Scan(&content); err != nil {
			return fmt.Errorf("load content: %w", err)
		}
		atts, err := listAttachments(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, a := range atts {
			path, err := s.blobPath(id, a.ID)
			if err != nil {
				return err
			}
			st, err := os.Stat(path)
			if err != nil {
				return fmt.Errorf("attachment blob: %w", err)
			}
			if st.Size() != a.Size {
				return fmt.Errorf("attachment blob %s/%s: size %d, want %d", id, a.ID, st.Size(), a.Size)
			}
		}
		refs, err := listImageRefs(ctx, tx, id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM pastes WHERE id = ?`, id); err != nil {
			return err
		}
		out = &Burned{Paste: p, Content: content, Attachments: atts, ImageRefs: refs}
		if len(atts) > 0 {
			dst := filepath.Join(s.tmpDir, "burn-"+id+"-"+NewPasteID())
			if err := os.Rename(s.pasteDir(id), dst); err != nil {
				return err
			}
			out.dir = dst
		}
		return nil
	})
	if err != nil {
		if out != nil && out.dir != "" {
			// Commit failed after the blobs moved: put them back.
			_ = os.Rename(out.dir, s.pasteDir(id))
		}
		return nil, err
	}
	if out.dir == "" {
		_ = s.removePasteDir(id)
	}
	return out, nil
}

// UpdatePaste stores a new version of the paste body and registers refs
// (image ref hashes) in the same transaction.
func (s *Store) UpdatePaste(ctx context.Context, id string, content []byte, baseVersion, maxVersions int, refs [][]byte) (*Paste, error) {
	var out *Paste
	err := s.write(ctx, func(tx *sql.Tx) error {
		p, err := s.getPaste(ctx, tx, id)
		if err != nil {
			return err
		}
		if p.Burn {
			return errInvalid("burn pastes cannot be edited")
		}
		if p.Version != baseVersion {
			return errStale(p.Version)
		}
		if p.Version >= maxVersions {
			return errTooLarge("version limit reached")
		}
		if err := s.addImageRefs(ctx, tx, id, refs); err != nil {
			return err
		}
		now := s.NowMs()
		if now < p.UpdatedAt {
			now = p.UpdatedAt
		}
		nv := p.Version + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO paste_versions (paste_id, version, created_at, content)
			VALUES (?, ?, ?, ?)`, id, nv, now, content); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE pastes SET version = ?, updated_at = ? WHERE id = ?`,
			nv, now, id); err != nil {
			return err
		}
		p.Version, p.UpdatedAt = nv, now
		out = p
		return nil
	})
	return out, err
}

// DeletePaste destroys a paste with everything attached to it.
func (s *Store) DeletePaste(ctx context.Context, id string) error {
	if !ValidPasteID(id) {
		return errNotFound("paste not found")
	}
	err := s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM pastes WHERE id = ? AND `+aliveCond, id, s.NowMs())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errNotFound("paste not found")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.removePasteDir(id)
}

// SetLocked locks or unlocks a paste's discussion.
func (s *Store) SetLocked(ctx context.Context, id string, locked bool) error {
	return s.write(ctx, func(tx *sql.Tx) error {
		p, err := s.getPaste(ctx, tx, id)
		if err != nil {
			return err
		}
		if !p.Discussion {
			return errInvalid("paste has no discussion")
		}
		_, err = tx.ExecContext(ctx, `UPDATE pastes SET locked = ? WHERE id = ?`, locked, id)
		return err
	})
}

// PasteVersions lists a live paste's versions (without content), ascending.
func (s *Store) PasteVersions(ctx context.Context, id string) ([]Version, error) {
	var out []Version
	err := s.View(ctx, func(v *View) error {
		if _, err := v.Paste(id); err != nil {
			return err
		}
		rows, err := v.tx.QueryContext(ctx, `SELECT version, created_at FROM paste_versions
			WHERE paste_id = ? ORDER BY version`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []Version{}
		for rows.Next() {
			var ver Version
			if err := rows.Scan(&ver.Version, &ver.CreatedAt); err != nil {
				return err
			}
			out = append(out, ver)
		}
		return rows.Err()
	})
	return out, err
}

// PasteVersion returns one version of a live paste.
func (s *Store) PasteVersion(ctx context.Context, id string, version int) (*Version, error) {
	var out *Version
	err := s.View(ctx, func(v *View) error {
		if _, err := v.Paste(id); err != nil {
			return err
		}
		ver := Version{Version: version}
		err := v.tx.QueryRowContext(ctx, `SELECT created_at, content FROM paste_versions
			WHERE paste_id = ? AND version = ?`, id, version).Scan(&ver.CreatedAt, &ver.Content)
		if errors.Is(err, sql.ErrNoRows) {
			return errNotFound("version not found")
		}
		if err != nil {
			return err
		}
		out = &ver
		return nil
	})
	return out, err
}

// SweepExpired deletes expired pastes (rows and blobs) and returns their IDs.
func (s *Store) SweepExpired(ctx context.Context) ([]string, error) {
	var all []string
	for {
		var batch []string
		err := s.write(ctx, func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, `DELETE FROM pastes WHERE id IN (
				SELECT id FROM pastes WHERE expires_at IS NOT NULL AND expires_at <= ? LIMIT 100
			) RETURNING id`, s.NowMs())
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				batch = append(batch, id)
			}
			return rows.Err()
		})
		if err != nil {
			return all, err
		}
		for _, id := range batch {
			_ = s.removePasteDir(id)
		}
		all = append(all, batch...)
		if len(batch) < 100 || ctx.Err() != nil {
			return all, ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Comments

// NewComment is the input to CreateComment.
type NewComment struct {
	ID        string
	ParentID  *string
	Author    string // "owner" | "guest"
	Verifier  []byte
	Tag       string
	Content   []byte
	ImageRefs [][]byte
}

func (s *Store) getComment(ctx context.Context, q querier, pasteID, id string) (*Comment, error) {
	c, err := scanComment(q.QueryRowContext(ctx, `SELECT `+commentCols+` `+commentFrom+`
		WHERE c.paste_id = ? AND c.id = ?`, pasteID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound("comment not found")
	}
	return c, err
}

// CreateComment adds a comment. asOwner reports that the request carried a
// valid owner token (owners may post while the discussion is locked).
func (s *Store) CreateComment(ctx context.Context, pasteID string, nc NewComment, asOwner bool, maxComments int) (*Comment, error) {
	if !ValidObjectID(nc.ID) || (nc.ParentID != nil && !ValidObjectID(*nc.ParentID)) {
		return nil, errInvalid("bad id")
	}
	if nc.Author != "owner" && nc.Author != "guest" {
		return nil, errInvalid("bad author")
	}
	var out *Comment
	err := s.write(ctx, func(tx *sql.Tx) error {
		p, err := s.getPaste(ctx, tx, pasteID)
		if err != nil {
			return err
		}
		if !p.Discussion {
			return errLocked("discussion is disabled")
		}
		if p.Locked && !asOwner {
			return errLocked("discussion is locked")
		}
		var one int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM comments WHERE paste_id = ? AND id = ?`, pasteID, nc.ID).Scan(&one)
		if err == nil {
			return errDuplicate()
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if nc.ParentID != nil {
			parent, err := s.getComment(ctx, tx, pasteID, *nc.ParentID)
			if IsCode(err, CodeNotFound) {
				return errInvalid("parent comment does not exist")
			} else if err != nil {
				return err
			}
			if parent.ParentID != nil {
				return errInvalid("replies can only be one level deep")
			}
			if parent.Deleted {
				return errInvalid("parent comment was deleted")
			}
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE paste_id = ?`, pasteID).Scan(&count); err != nil {
			return err
		}
		if count >= maxComments {
			return errTooLarge("comment limit reached")
		}
		if err := s.addImageRefs(ctx, tx, pasteID, nc.ImageRefs); err != nil {
			return err
		}
		now := s.NowMs()
		var parent any
		if nc.ParentID != nil {
			parent = *nc.ParentID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO comments
			(paste_id, id, parent_id, author, author_verifier, tag, created_at, updated_at, version, deleted)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1, 0)`,
			pasteID, nc.ID, parent, nc.Author, nc.Verifier, nc.Tag, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO comment_versions (paste_id, comment_id, version, created_at, content)
			VALUES (?, ?, 1, ?, ?)`, pasteID, nc.ID, now, nc.Content); err != nil {
			return err
		}
		out = &Comment{
			ID: nc.ID, ParentID: nc.ParentID, Author: nc.Author, Verifier: nc.Verifier, Tag: nc.Tag,
			CreatedAt: now, UpdatedAt: now, Version: 1, Content: nc.Content,
		}
		return nil
	})
	return out, err
}

// GetComment returns a comment of a live paste (tombstones included).
func (s *Store) GetComment(ctx context.Context, pasteID, id string) (*Comment, error) {
	if !ValidObjectID(id) {
		return nil, errNotFound("comment not found")
	}
	var out *Comment
	err := s.View(ctx, func(v *View) error {
		if _, err := v.Paste(pasteID); err != nil {
			return err
		}
		c, err := s.getComment(ctx, v.tx, pasteID, id)
		out = c
		return err
	})
	return out, err
}

// UpdateComment stores a new version of a comment. authorize is called
// inside the transaction with the current comment and must return an error
// (typically ErrForbidden) to deny the edit.
func (s *Store) UpdateComment(ctx context.Context, pasteID, id string, content []byte, baseVersion, maxVersions int,
	refs [][]byte, authorize func(c *Comment) error) (*Comment, error) {
	if !ValidObjectID(id) {
		return nil, errNotFound("comment not found")
	}
	var out *Comment
	err := s.write(ctx, func(tx *sql.Tx) error {
		p, err := s.getPaste(ctx, tx, pasteID)
		if err != nil {
			return err
		}
		c, err := s.getComment(ctx, tx, pasteID, id)
		if err != nil {
			return err
		}
		if err := authorize(c); err != nil {
			return err
		}
		if p.Locked && c.Author != "owner" {
			return errLocked("discussion is locked")
		}
		if c.Deleted {
			return errInvalid("comment was deleted")
		}
		if c.Version != baseVersion {
			return errStale(c.Version)
		}
		if c.Version >= maxVersions {
			return errTooLarge("version limit reached")
		}
		if err := s.addImageRefs(ctx, tx, pasteID, refs); err != nil {
			return err
		}
		now := s.NowMs()
		if now < c.UpdatedAt {
			now = c.UpdatedAt
		}
		nv := c.Version + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO comment_versions (paste_id, comment_id, version, created_at, content)
			VALUES (?, ?, ?, ?, ?)`, pasteID, id, nv, now, content); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE comments SET version = ?, updated_at = ? WHERE paste_id = ? AND id = ?`,
			nv, now, pasteID, id); err != nil {
			return err
		}
		c.Version, c.UpdatedAt, c.Content = nv, now, content
		out = c
		return nil
	})
	return out, err
}

// DeleteResult describes the effect of DeleteComment.
type DeleteResult struct {
	// Tombstoned is set when a top-level comment with replies became a
	// tombstone.
	Tombstoned *Comment
	// Removed lists comment IDs removed entirely, in removal order (a reply,
	// then possibly its now-empty tombstone parent).
	Removed []string
}

// DeleteComment deletes a comment following the tombstone rules of
// DESIGN.md §4. authorize is called inside the transaction.
func (s *Store) DeleteComment(ctx context.Context, pasteID, id string, authorize func(c *Comment) error) (*DeleteResult, error) {
	if !ValidObjectID(id) {
		return nil, errNotFound("comment not found")
	}
	var res DeleteResult
	var blobs []string
	err := s.write(ctx, func(tx *sql.Tx) error {
		if _, err := s.getPaste(ctx, tx, pasteID); err != nil {
			return err
		}
		c, err := s.getComment(ctx, tx, pasteID, id)
		if err != nil {
			return err
		}
		if c.Deleted {
			return errNotFound("comment not found")
		}
		if err := authorize(c); err != nil {
			return err
		}
		dropAttachments := func(cid string) error {
			rows, err := tx.QueryContext(ctx, `DELETE FROM attachments WHERE paste_id = ? AND comment_id = ? RETURNING id`,
				pasteID, cid)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var aid string
				if err := rows.Scan(&aid); err != nil {
					return err
				}
				if p, err := s.blobPath(pasteID, aid); err == nil {
					blobs = append(blobs, p)
				}
			}
			return rows.Err()
		}
		remove := func(cid string) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM comments WHERE paste_id = ? AND id = ?`, pasteID, cid); err != nil {
				return err
			}
			res.Removed = append(res.Removed, cid)
			return dropAttachments(cid)
		}
		countReplies := func(cid string) (int, error) {
			var n int
			err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM comments WHERE paste_id = ? AND parent_id = ?`,
				pasteID, cid).Scan(&n)
			return n, err
		}

		if c.ParentID == nil {
			n, err := countReplies(id)
			if err != nil {
				return err
			}
			if n == 0 {
				return remove(id)
			}
			now := s.NowMs()
			if now < c.UpdatedAt {
				now = c.UpdatedAt
			}
			if _, err := tx.ExecContext(ctx, `UPDATE comments SET deleted = 1, updated_at = ? WHERE paste_id = ? AND id = ?`,
				now, pasteID, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM comment_versions WHERE paste_id = ? AND comment_id = ?`,
				pasteID, id); err != nil {
				return err
			}
			if err := dropAttachments(id); err != nil {
				return err
			}
			c.Deleted, c.Content, c.UpdatedAt = true, nil, now
			res.Tombstoned = c
			return nil
		}

		if err := remove(id); err != nil {
			return err
		}
		parent, err := s.getComment(ctx, tx, pasteID, *c.ParentID)
		if IsCode(err, CodeNotFound) {
			return nil
		} else if err != nil {
			return err
		}
		if parent.Deleted {
			n, err := countReplies(parent.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				return remove(parent.ID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, b := range blobs {
		_ = os.Remove(b)
	}
	return &res, nil
}

// CommentVersions returns the full history of a (non-deleted) comment.
func (s *Store) CommentVersions(ctx context.Context, pasteID, id string) ([]Version, error) {
	if !ValidObjectID(id) {
		return nil, errNotFound("comment not found")
	}
	var out []Version
	err := s.View(ctx, func(v *View) error {
		if _, err := v.Paste(pasteID); err != nil {
			return err
		}
		c, err := s.getComment(ctx, v.tx, pasteID, id)
		if err != nil {
			return err
		}
		if c.Deleted {
			return errNotFound("comment was deleted")
		}
		rows, err := v.tx.QueryContext(ctx, `SELECT version, created_at, content FROM comment_versions
			WHERE paste_id = ? AND comment_id = ? ORDER BY version`, pasteID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []Version{}
		for rows.Next() {
			var ver Version
			if err := rows.Scan(&ver.Version, &ver.CreatedAt, &ver.Content); err != nil {
				return err
			}
			out = append(out, ver)
		}
		return rows.Err()
	})
	return out, err
}

// ---------------------------------------------------------------------------
// Attachments

// AttachmentLimits bound a paste's attachments.
type AttachmentLimits struct {
	MaxTotal int64 // sum of data sizes
	MaxCount int
}

// CreateAttachment adds an attachment to an existing paste, moving its blob
// into place. asOwner reports a valid owner token on the request.
func (s *Store) CreateAttachment(ctx context.Context, pasteID string, nb NewBlob, asOwner bool, lim AttachmentLimits) (*Attachment, error) {
	if !ValidObjectID(nb.ID) || (nb.CommentID != nil && !ValidObjectID(*nb.CommentID)) {
		return nil, errInvalid("bad id")
	}
	final, err := s.blobPath(pasteID, nb.ID)
	if err != nil {
		return nil, errNotFound("paste not found")
	}
	var out *Attachment
	moved := false
	err = s.write(ctx, func(tx *sql.Tx) error {
		p, err := s.getPaste(ctx, tx, pasteID)
		if err != nil {
			return err
		}
		if p.Burn {
			return errInvalid("burn pastes cannot be changed")
		}
		if nb.CommentID == nil {
			if !asOwner {
				return ErrForbidden
			}
		} else {
			if !p.Discussion {
				return errLocked("discussion is disabled")
			}
			if p.Locked && !asOwner {
				return errLocked("discussion is locked")
			}
			c, err := s.getComment(ctx, tx, pasteID, *nb.CommentID)
			if err == nil && c.Deleted {
				return errInvalid("comment was deleted")
			} else if err != nil && !IsCode(err, CodeNotFound) {
				return err
			}
		}
		var one int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM attachments WHERE paste_id = ? AND id = ?`, pasteID, nb.ID).Scan(&one)
		if err == nil {
			return errDuplicate()
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var total int64
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0), COUNT(*) FROM attachments WHERE paste_id = ?`,
			pasteID).Scan(&total, &count); err != nil {
			return err
		}
		if total+nb.Size > lim.MaxTotal {
			return errTooLarge("paste attachment quota exceeded")
		}
		if count >= lim.MaxCount {
			return errTooLarge("too many attachments")
		}
		now := s.NowMs()
		var cid any
		if nb.CommentID != nil {
			cid = *nb.CommentID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO attachments (paste_id, id, comment_id, size, meta, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, pasteID, nb.ID, cid, nb.Size, nb.Meta, now); err != nil {
			return err
		}
		if err := os.MkdirAll(s.pasteDir(pasteID), 0o700); err != nil {
			return err
		}
		if err := os.Rename(nb.TmpPath, final); err != nil {
			return err
		}
		moved = true
		out = &Attachment{ID: nb.ID, CommentID: nb.CommentID, Size: nb.Size, Meta: nb.Meta, CreatedAt: now}
		return nil
	})
	if err != nil {
		if moved {
			_ = os.Remove(final)
		}
		return nil, err
	}
	return out, nil
}

// OpenAttachment returns an open handle on an attachment blob of a live
// paste. The caller must close it.
func (s *Store) OpenAttachment(ctx context.Context, pasteID, id string) (*os.File, *Attachment, error) {
	path, err := s.blobPath(pasteID, id)
	if err != nil {
		return nil, nil, errNotFound("attachment not found")
	}
	var a Attachment
	var cid sql.NullString
	err = s.r.QueryRowContext(ctx, `SELECT a.id, a.comment_id, a.size, a.meta, a.created_at
		FROM attachments a JOIN pastes p ON p.id = a.paste_id
		WHERE a.paste_id = ? AND a.id = ? AND (p.expires_at IS NULL OR p.expires_at > ?)`,
		pasteID, id, s.NowMs()).Scan(&a.ID, &cid, &a.Size, &a.Meta, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, errNotFound("attachment not found")
	}
	if err != nil {
		return nil, nil, err
	}
	if cid.Valid {
		v := cid.String
		a.CommentID = &v
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, errNotFound("attachment not found")
	}
	if err != nil {
		return nil, nil, err
	}
	return f, &a, nil
}
