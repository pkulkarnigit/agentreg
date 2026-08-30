// Package sqlite implements store.MetadataStore on top of a local SQLite
// file (via the pure-Go modernc.org/sqlite driver, so apreg-server ships as
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
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	username      TEXT NOT NULL UNIQUE,
	email         TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	created_at    TEXT NOT NULL
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
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	scope         TEXT NOT NULL,
	name          TEXT NOT NULL,
	version       TEXT NOT NULL,
	checksum      TEXT NOT NULL,
	manifest_json TEXT NOT NULL,
	published_at  TEXT NOT NULL,
	UNIQUE(scope, name, version)
);

CREATE TABLE IF NOT EXISTS keywords (
	scope   TEXT NOT NULL,
	name    TEXT NOT NULL,
	keyword TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_keywords_lookup ON keywords(scope, name);
`

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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

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
		`SELECT id, username, email, password_hash, created_at FROM users WHERE username = ?`, username)
	var u store.User
	var createdAt string
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &createdAt); err != nil {
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
		SELECT u.id, u.username, u.email, u.password_hash, u.created_at
		FROM tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, tokenHash)
	var u store.User
	var createdAt string
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &createdAt); err != nil {
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

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO versions (scope, name, version, checksum, manifest_json, published_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		v.Scope, v.Name, v.Version, v.Checksum, v.ManifestJSON, now,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) GetPlugin(ctx context.Context, scope, name string) (*store.Plugin, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT scope, name, description, homepage, repository, license, latest_version, created_at, updated_at
		FROM plugins WHERE scope = ? AND name = ?`, scope, name)
	var p store.Plugin
	var createdAt, updatedAt string
	if err := row.Scan(&p.Scope, &p.Name, &p.Description, &p.Homepage, &p.Repository, &p.License,
		&p.LatestVersion, &createdAt, &updatedAt); err != nil {
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
		SELECT scope, name, version, checksum, manifest_json, published_at
		FROM versions WHERE scope = ? AND name = ? AND version = ?`, scope, name, version)
	var v store.Version
	var publishedAt string
	if err := row.Scan(&v.Scope, &v.Name, &v.Version, &v.Checksum, &v.ManifestJSON, &publishedAt); err != nil {
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
		SELECT scope, name, version, checksum, manifest_json, published_at
		FROM versions WHERE scope = ? AND name = ? ORDER BY published_at ASC`, scope, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Version
	for rows.Next() {
		var v store.Version
		var publishedAt string
		if err := rows.Scan(&v.Scope, &v.Name, &v.Version, &v.Checksum, &v.ManifestJSON, &publishedAt); err != nil {
			return nil, err
		}
		v.PublishedAt, _ = time.Parse(time.RFC3339, publishedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Search(ctx context.Context, query string) ([]store.Plugin, error) {
	like := "%" + strings.ToLower(query) + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT p.scope, p.name, p.description, p.homepage, p.repository, p.license,
		       p.latest_version, p.created_at, p.updated_at
		FROM plugins p
		LEFT JOIN keywords k ON k.scope = p.scope AND k.name = p.name
		WHERE lower(p.scope || '/' || p.name) LIKE ?
		   OR lower(p.description) LIKE ?
		   OR lower(k.keyword) LIKE ?
		ORDER BY p.updated_at DESC`, like, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Plugin
	for rows.Next() {
		var p store.Plugin
		var createdAt, updatedAt string
		if err := rows.Scan(&p.Scope, &p.Name, &p.Description, &p.Homepage, &p.Repository, &p.License,
			&p.LatestVersion, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
		p.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

var _ store.MetadataStore = (*Store)(nil)
