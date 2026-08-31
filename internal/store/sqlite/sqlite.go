// Package sqlite implements store.MetadataStore on top of a local SQLite
// file (via the pure-Go modernc.org/sqlite driver, so krate-server ships as
// a single static binary with no cgo/libsqlite3 dependency).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pkulkarni/apreg/internal/store"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS users (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	username       TEXT NOT NULL UNIQUE,
	email          TEXT NOT NULL UNIQUE,
	password_hash  TEXT NOT NULL,
	email_verified INTEGER NOT NULL DEFAULT 0,
	created_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tokens (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id       INTEGER NOT NULL REFERENCES users(id),
	token_hash    TEXT NOT NULL UNIQUE,
	label         TEXT NOT NULL,
	created_at    TEXT NOT NULL,
	last_used_at  TEXT
);

CREATE TABLE IF NOT EXISTS plugins (
	scope          TEXT NOT NULL,
	name           TEXT NOT NULL,
	description    TEXT NOT NULL DEFAULT '',
	homepage       TEXT NOT NULL DEFAULT '',
	repository     TEXT NOT NULL DEFAULT '',
	license        TEXT NOT NULL DEFAULT '',
	latest_version TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL,
	updated_at     TEXT NOT NULL,
	PRIMARY KEY (scope, name)
);

CREATE TABLE IF NOT EXISTS versions (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	scope          TEXT NOT NULL,
	name           TEXT NOT NULL,
	version        TEXT NOT NULL,
	checksum       TEXT NOT NULL,
	manifest_json  TEXT NOT NULL,
	published_at   TEXT NOT NULL,
	download_count INTEGER NOT NULL DEFAULT 0,
	UNIQUE(scope, name, version)
);

CREATE TABLE IF NOT EXISTS keywords (
	scope   TEXT NOT NULL,
	name    TEXT NOT NULL,
	keyword TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_keywords_lookup ON keywords(scope, name);

CREATE TABLE IF NOT EXISTS email_verifications (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS password_resets (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id),
	expires_at TEXT NOT NULL,
	used_at    TEXT
);

-- Full-text index over plugins(scope, name, description), replacing a
-- leading-wildcard LIKE '%...%' in Search, which can't use an index and
-- forces a full table scan on every request. External-content FTS5 table
-- (content='plugins') keeps the indexed text out of plugins itself; the
-- three triggers below are the standard SQLite-documented way to keep it
-- in sync with every insert/update/delete, since content='plugins' tables
-- aren't maintained automatically. Keywords aren't included here — they're
-- a separate one-to-many table and stay on a plain LIKE in Search, which is
-- fine given how few keywords a single plugin actually has.
CREATE VIRTUAL TABLE IF NOT EXISTS plugins_fts USING fts5(
	scope, name, description,
	content='plugins', content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS plugins_fts_ai AFTER INSERT ON plugins BEGIN
	INSERT INTO plugins_fts(rowid, scope, name, description) VALUES (new.rowid, new.scope, new.name, new.description);
END;

CREATE TRIGGER IF NOT EXISTS plugins_fts_ad AFTER DELETE ON plugins BEGIN
	INSERT INTO plugins_fts(plugins_fts, rowid, scope, name, description) VALUES ('delete', old.rowid, old.scope, old.name, old.description);
END;

CREATE TRIGGER IF NOT EXISTS plugins_fts_au AFTER UPDATE ON plugins BEGIN
	INSERT INTO plugins_fts(plugins_fts, rowid, scope, name, description) VALUES ('delete', old.rowid, old.scope, old.name, old.description);
	INSERT INTO plugins_fts(rowid, scope, name, description) VALUES (new.rowid, new.scope, new.name, new.description);
END;

CREATE TABLE IF NOT EXISTS schema_migrations (
	name       TEXT PRIMARY KEY,
	applied_at TEXT NOT NULL
);
`

// backfillFTSMigration is the name recorded in schema_migrations once the
// full-text index has been backfilled — see runBackfillFTS.
const backfillFTSMigration = "plugins_fts_backfill"

type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) a SQLite database at path and ensures
// the schema exists.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// SQLite allows only one writer at a time; a single connection avoids
	// SQLITE_BUSY errors under concurrent goroutines within this process.
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := runBackfillFTS(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill search index: %w", err)
	}
	return &Store{db: db}, nil
}

// runBackfillFTS indexes any plugins rows that predate the plugins_fts
// table/triggers — those only cover writes from here on, so a database
// that already had rows before this migration landed would otherwise have
// them permanently missing from search.
//
// This can't be a plain "insert what's missing" query the way the rest of
// this file's migrations are: for an external-content FTS5 table (one
// backed by content='plugins' rather than storing its own copy of the
// text), a bare SELECT against plugins_fts with no MATCH clause reads
// straight through to the plugins table itself rather than reflecting
// what's actually in the FTS index — so a "WHERE rowid NOT IN (SELECT
// rowid FROM plugins_fts)" guard always looks like everything's already
// indexed even when the index is completely empty. The real fix is
// FTS5's documented 'rebuild' command, which reindexes from the content
// table directly; schema_migrations just makes sure that full reindex
// runs exactly once rather than on every process start.
func runBackfillFTS(db *sql.DB) error {
	res, err := db.Exec(
		`INSERT OR IGNORE INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		backfillFTSMigration, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err // already backfilled in a previous Open()
	}
	_, err = db.Exec(`INSERT INTO plugins_fts(plugins_fts) VALUES ('rebuild')`)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// Backup writes a consistent point-in-time snapshot of the database to
// path via SQLite's VACUUM INTO, safe to run against a live database.
func (s *Store) Backup(path string) error {
	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	return err
}

func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash string) (*store.User, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, email, password_hash, created_at) VALUES (?, ?, ?, ?)`,
		username, email, passwordHash, now.Format(time.RFC3339),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, store.ErrConflict
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &store.User{ID: id, Username: username, Email: email, PasswordHash: passwordHash, CreatedAt: now}, nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*store.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, password_hash, email_verified, created_at FROM users WHERE username = ?`, username)
	var u store.User
	var createdAt string
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.EmailVerified, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &u, nil
}

func (s *Store) CreateToken(ctx context.Context, userID int64, tokenHash, label string) (*store.Token, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (user_id, token_hash, label, created_at) VALUES (?, ?, ?, ?)`,
		userID, tokenHash, label, now.Format(time.RFC3339),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, store.ErrConflict
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &store.Token{ID: id, UserID: userID, Label: label, CreatedAt: now}, nil
}

func (s *Store) GetUserByTokenHash(ctx context.Context, tokenHash string) (*store.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.email, u.password_hash, u.email_verified, u.created_at
		FROM tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, tokenHash)
	var u store.User
	var createdAt string
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.EmailVerified, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)

	_, _ = s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = ? WHERE token_hash = ?`,
		time.Now().UTC().Format(time.RFC3339), tokenHash)

	return &u, nil
}

func (s *Store) UpsertPluginAndVersion(ctx context.Context, p store.NewPlugin, v store.NewVersion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM versions WHERE scope = ? AND name = ? AND version = ?`,
		v.Scope, v.Name, v.Version,
	).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return store.ErrConflict
	}

	var pluginExists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM plugins WHERE scope = ? AND name = ?`, p.Scope, p.Name,
	).Scan(&pluginExists); err != nil {
		return err
	}
	if pluginExists == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO plugins (scope, name, description, homepage, repository, license, latest_version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Scope, p.Name, p.Description, p.Homepage, p.Repository, p.License, v.Version, now, now,
		); err != nil {
			return err
		}
		for _, kw := range p.Keywords {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO keywords (scope, name, keyword) VALUES (?, ?, ?)`, p.Scope, p.Name, kw,
			); err != nil {
				return err
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE plugins SET description = ?, homepage = ?, repository = ?, license = ?, latest_version = ?, updated_at = ?
			WHERE scope = ? AND name = ?`,
			p.Description, p.Homepage, p.Repository, p.License, v.Version, now, p.Scope, p.Name,
		); err != nil {
			return err
		}
	}

	publishedAt := now
	if !v.PublishedAt.IsZero() {
		publishedAt = v.PublishedAt.UTC().Format(time.RFC3339)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO versions (scope, name, version, checksum, manifest_json, published_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		v.Scope, v.Name, v.Version, v.Checksum, v.ManifestJSON, publishedAt,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) UpdateVersionPublishedAt(ctx context.Context, scope, name, version string, publishedAt time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE versions SET published_at = ? WHERE scope = ? AND name = ? AND version = ?`,
		publishedAt.UTC().Format(time.RFC3339), scope, name, version,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) GetPlugin(ctx context.Context, scope, name string) (*store.Plugin, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT p.scope, p.name, p.description, p.homepage, p.repository, p.license, p.latest_version,
		       p.created_at, p.updated_at, COALESCE((SELECT SUM(download_count) FROM versions v WHERE v.scope = p.scope AND v.name = p.name), 0)
		FROM plugins p WHERE p.scope = ? AND p.name = ?`, scope, name)
	var p store.Plugin
	var createdAt, updatedAt string
	if err := row.Scan(&p.Scope, &p.Name, &p.Description, &p.Homepage, &p.Repository, &p.License,
		&p.LatestVersion, &createdAt, &updatedAt, &p.TotalDownloads); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)

	rows, err := s.db.QueryContext(ctx, `SELECT keyword FROM keywords WHERE scope = ? AND name = ?`, scope, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kw string
		if err := rows.Scan(&kw); err != nil {
			return nil, err
		}
		p.Keywords = append(p.Keywords, kw)
	}
	return &p, rows.Err()
}

func (s *Store) GetVersion(ctx context.Context, scope, name, version string) (*store.Version, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT scope, name, version, checksum, manifest_json, published_at, download_count
		FROM versions WHERE scope = ? AND name = ? AND version = ?`, scope, name, version)
	var v store.Version
	var publishedAt string
	if err := row.Scan(&v.Scope, &v.Name, &v.Version, &v.Checksum, &v.ManifestJSON, &publishedAt, &v.DownloadCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	v.PublishedAt, _ = time.Parse(time.RFC3339, publishedAt)
	return &v, nil
}

func (s *Store) ListVersions(ctx context.Context, scope, name string) ([]store.Version, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, name, version, checksum, manifest_json, published_at, download_count
		FROM versions WHERE scope = ? AND name = ? ORDER BY published_at ASC`, scope, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Version
	for rows.Next() {
		var v store.Version
		var publishedAt string
		if err := rows.Scan(&v.Scope, &v.Name, &v.Version, &v.Checksum, &v.ManifestJSON, &publishedAt, &v.DownloadCount); err != nil {
			return nil, err
		}
		v.PublishedAt, _ = time.Parse(time.RFC3339, publishedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

// fts5MatchQuery turns raw search-box text into a safe FTS5 MATCH
// expression. FTS5's own query syntax gives special meaning to quotes,
// hyphens, colons, and boolean keywords — passing user input straight
// through would let a query like `foo -bar` silently mean "exclude bar"
// instead of a literal search for those two words, or error outright on
// something like a bare `"`. Quoting each whitespace-separated token as
// its own FTS5 string literal (doubling any embedded quote, FTS5's own
// escape rule) makes every token literal; consecutive quoted tokens with
// no explicit operator between them default to AND in FTS5, which is the
// same "all these words" semantics the old LIKE-based search had.
func fts5MatchQuery(query string) string {
	fields := strings.Fields(query)
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(parts, " ")
}

func (s *Store) IncrementDownloadCount(ctx context.Context, scope, name, version string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE versions SET download_count = download_count + 1 WHERE scope = ? AND name = ? AND version = ?`,
		scope, name, version)
	return err
}

func (s *Store) Search(ctx context.Context, query string) ([]store.Plugin, error) {
	query = strings.TrimSpace(query)

	var rows *sql.Rows
	var err error
	if query == "" {
		// No query: browse-all, same as the old LIKE '%%' behavior — an
		// empty FTS5 MATCH would error, not match everything, so this has
		// to stay a separate path rather than falling through below.
		rows, err = s.db.QueryContext(ctx, `
			SELECT p.scope, p.name, p.description, p.homepage, p.repository, p.license,
			       p.latest_version, p.created_at, p.updated_at,
			       COALESCE((SELECT SUM(download_count) FROM versions v WHERE v.scope = p.scope AND v.name = p.name), 0)
			FROM plugins p
			ORDER BY p.updated_at DESC`)
	} else {
		like := "%" + strings.ToLower(query) + "%"
		rows, err = s.db.QueryContext(ctx, `
			SELECT DISTINCT p.scope, p.name, p.description, p.homepage, p.repository, p.license,
			       p.latest_version, p.created_at, p.updated_at,
			       COALESCE((SELECT SUM(download_count) FROM versions v WHERE v.scope = p.scope AND v.name = p.name), 0)
			FROM plugins p
			LEFT JOIN keywords k ON k.scope = p.scope AND k.name = p.name
			WHERE p.rowid IN (SELECT rowid FROM plugins_fts WHERE plugins_fts MATCH ?)
			   OR lower(k.keyword) LIKE ?
			ORDER BY p.updated_at DESC`, fts5MatchQuery(query), like)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Plugin
	for rows.Next() {
		var p store.Plugin
		var createdAt, updatedAt string
		if err := rows.Scan(&p.Scope, &p.Name, &p.Description, &p.Homepage, &p.Repository, &p.License,
			&p.LatestVersion, &createdAt, &updatedAt, &p.TotalDownloads); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Keywords, like GetPlugin, are fetched per-plugin rather than
	// aggregated in the main query — SQLite/Postgres have different
	// string-aggregation syntax, and result sets here are small (dozens
	// of plugins, not millions), so N+1 here trades a little query volume
	// for identical, portable code across both backends.
	for i := range out {
		kwRows, err := s.db.QueryContext(ctx, `SELECT keyword FROM keywords WHERE scope = ? AND name = ?`, out[i].Scope, out[i].Name)
		if err != nil {
			return nil, err
		}
		for kwRows.Next() {
			var kw string
			if err := kwRows.Scan(&kw); err != nil {
				kwRows.Close()
				return nil, err
			}
			out[i].Keywords = append(out[i].Keywords, kw)
		}
		if err := kwRows.Err(); err != nil {
			kwRows.Close()
			return nil, err
		}
		kwRows.Close()
	}

	return out, nil
}

func (s *Store) CreateEmailVerification(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO email_verifications (token_hash, user_id, expires_at) VALUES (?, ?, ?)`,
		tokenHash, userID, expiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil && isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) ConsumeEmailVerification(ctx context.Context, tokenHash string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var userID int64
	var expiresAt string
	err = tx.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM email_verifications WHERE token_hash = ?`, tokenHash,
	).Scan(&userID, &expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, store.ErrTokenInvalid
		}
		return 0, err
	}
	exp, _ := time.Parse(time.RFC3339, expiresAt)
	if time.Now().UTC().After(exp) {
		return 0, store.ErrTokenInvalid
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM email_verifications WHERE token_hash = ?`, tokenHash); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET email_verified = 1 WHERE id = ?`, userID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}

func (s *Store) CreatePasswordReset(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO password_resets (token_hash, user_id, expires_at, used_at) VALUES (?, ?, ?, NULL)`,
		tokenHash, userID, expiresAt.UTC().Format(time.RFC3339),
	)
	if err != nil && isUniqueViolation(err) {
		return store.ErrConflict
	}
	return err
}

func (s *Store) ConsumePasswordReset(ctx context.Context, tokenHash string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var userID int64
	var expiresAt string
	var usedAt sql.NullString
	err = tx.QueryRowContext(ctx,
		`SELECT user_id, expires_at, used_at FROM password_resets WHERE token_hash = ?`, tokenHash,
	).Scan(&userID, &expiresAt, &usedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, store.ErrTokenInvalid
		}
		return 0, err
	}
	if usedAt.Valid {
		return 0, store.ErrTokenInvalid
	}
	exp, _ := time.Parse(time.RFC3339, expiresAt)
	if time.Now().UTC().After(exp) {
		return 0, store.ErrTokenInvalid
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE password_resets SET used_at = ? WHERE token_hash = ?`,
		time.Now().UTC().Format(time.RFC3339), tokenHash,
	); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}

func (s *Store) UpdatePassword(ctx context.Context, userID int64, newPasswordHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, newPasswordHash, userID)
	return err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

var _ store.MetadataStore = (*Store)(nil)
