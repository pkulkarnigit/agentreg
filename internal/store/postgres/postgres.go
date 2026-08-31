// Package postgres implements store.MetadataStore on top of Postgres (via
// github.com/jackc/pgx/v5/stdlib, registered as a database/sql driver so
// this mirrors internal/store/sqlite's shape closely — same method
// signatures, same transaction-based upsert logic — just Postgres-flavored
// SQL. This is the backend docker-compose.yml runs in anything resembling
// a real deployment; SQLite stays the zero-dependency default for plain
// `go run`/binary usage.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/pkulkarni/apreg/internal/store"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS users (
	id             BIGSERIAL PRIMARY KEY,
	username       TEXT NOT NULL UNIQUE,
	email          TEXT NOT NULL UNIQUE,
	password_hash  TEXT NOT NULL,
	email_verified BOOLEAN NOT NULL DEFAULT FALSE,
	created_at     TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS tokens (
	id            BIGSERIAL PRIMARY KEY,
	user_id       BIGINT NOT NULL REFERENCES users(id),
	token_hash    TEXT NOT NULL UNIQUE,
	label         TEXT NOT NULL,
	created_at    TIMESTAMPTZ NOT NULL,
	last_used_at  TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS plugins (
	scope          TEXT NOT NULL,
	name           TEXT NOT NULL,
	description    TEXT NOT NULL DEFAULT '',
	homepage       TEXT NOT NULL DEFAULT '',
	repository     TEXT NOT NULL DEFAULT '',
	license        TEXT NOT NULL DEFAULT '',
	latest_version TEXT NOT NULL DEFAULT '',
	created_at     TIMESTAMPTZ NOT NULL,
	updated_at     TIMESTAMPTZ NOT NULL,
	PRIMARY KEY (scope, name)
);

CREATE TABLE IF NOT EXISTS versions (
	id             BIGSERIAL PRIMARY KEY,
	scope          TEXT NOT NULL,
	name           TEXT NOT NULL,
	version        TEXT NOT NULL,
	checksum       TEXT NOT NULL,
	manifest_json  TEXT NOT NULL,
	published_at   TIMESTAMPTZ NOT NULL,
	download_count BIGINT NOT NULL DEFAULT 0,
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
	user_id    BIGINT NOT NULL REFERENCES users(id),
	expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS password_resets (
	token_hash TEXT PRIMARY KEY,
	user_id    BIGINT NOT NULL REFERENCES users(id),
	expires_at TIMESTAMPTZ NOT NULL,
	used_at    TIMESTAMPTZ
);
`

// migrateSearchVectorSQL adds a full-text index over
// plugins(scope, name, description), replacing a leading-wildcard
// LIKE '%...%' in Search, which can't use a B-tree index and forces a
// sequential scan on every request. It's a separate ALTER TABLE rather
// than baked into schemaSQL's CREATE TABLE above because CREATE TABLE IF
// NOT EXISTS is a no-op against a database that already has the plugins
// table — this runs on every Open() and, being a GENERATED ALWAYS ...
// STORED column, Postgres backfills it for every existing row the moment
// the column is added, no separate backfill needed the way SQLite's FTS5
// (an external-content table, not a generated column) requires. Keywords
// aren't included — they're a separate one-to-many table and stay on a
// plain LIKE in Search, which is fine given how few keywords a single
// plugin actually has.
const migrateSearchVectorSQL = `
ALTER TABLE plugins ADD COLUMN IF NOT EXISTS search_vector tsvector
	GENERATED ALWAYS AS (to_tsvector('simple', scope || ' ' || name || ' ' || description)) STORED;

CREATE INDEX IF NOT EXISTS idx_plugins_search_vector ON plugins USING GIN (search_vector);
`

type Store struct {
	db *sql.DB
}

// Connection pool limits. database/sql's own defaults are unbounded open
// connections and only 2 idle — fine for one process talking to its own
// database, but not for a store meant to run behind N horizontally scaled
// krate-server replicas: every replica holds this many connections
// independently, so the total against one Postgres instance is
// maxOpenConns × replica count, and it must stay comfortably under
// Postgres's own max_connections (100 by default). At 10 per replica,
// that's headroom for around 8 replicas before either number needs
// raising. connMaxLifetime recycles connections periodically so a
// long-running replica doesn't accumulate ones a load balancer or
// Postgres-side failover has silently dropped.
const (
	maxOpenConns    = 10
	maxIdleConns    = 5
	connMaxLifetime = 30 * time.Minute
	connMaxIdleTime = 5 * time.Minute
)

// Open connects to Postgres at dsn (a "postgres://..." URL) and ensures the
// schema exists.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxIdleConns)
	db.SetConnMaxLifetime(connMaxLifetime)
	db.SetConnMaxIdleTime(connMaxIdleTime)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.Exec(migrateSearchVectorSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate search index: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CreateUser(ctx context.Context, username, email, passwordHash string) (*store.User, error) {
	now := time.Now().UTC()
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO users (username, email, password_hash, created_at) VALUES ($1, $2, $3, $4) RETURNING id`,
		username, email, passwordHash, now,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, store.ErrConflict
		}
		return nil, err
	}
	return &store.User{ID: id, Username: username, Email: email, PasswordHash: passwordHash, CreatedAt: now}, nil
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*store.User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, username, email, password_hash, email_verified, created_at FROM users WHERE username = $1`, username)
	var u store.User
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.EmailVerified, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return &u, nil
}

func (s *Store) CreateToken(ctx context.Context, userID int64, tokenHash, label string) (*store.Token, error) {
	now := time.Now().UTC()
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO tokens (user_id, token_hash, label, created_at) VALUES ($1, $2, $3, $4) RETURNING id`,
		userID, tokenHash, label, now,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, store.ErrConflict
		}
		return nil, err
	}
	return &store.Token{ID: id, UserID: userID, Label: label, CreatedAt: now}, nil
}

func (s *Store) GetUserByTokenHash(ctx context.Context, tokenHash string) (*store.User, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.email, u.password_hash, u.email_verified, u.created_at
		FROM tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = $1`, tokenHash)
	var u store.User
	if err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.EmailVerified, &u.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	u.CreatedAt = u.CreatedAt.UTC()

	_, _ = s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at = $1 WHERE token_hash = $2`,
		time.Now().UTC(), tokenHash)

	return &u, nil
}

func (s *Store) UpsertPluginAndVersion(ctx context.Context, p store.NewPlugin, v store.NewVersion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM versions WHERE scope = $1 AND name = $2 AND version = $3`,
		v.Scope, v.Name, v.Version,
	).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return store.ErrConflict
	}

	var pluginExists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM plugins WHERE scope = $1 AND name = $2`, p.Scope, p.Name,
	).Scan(&pluginExists); err != nil {
		return err
	}
	if pluginExists == 0 {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO plugins (scope, name, description, homepage, repository, license, latest_version, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			p.Scope, p.Name, p.Description, p.Homepage, p.Repository, p.License, v.Version, now, now,
		); err != nil {
			return err
		}
		for _, kw := range p.Keywords {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO keywords (scope, name, keyword) VALUES ($1, $2, $3)`, p.Scope, p.Name, kw,
			); err != nil {
				return err
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE plugins SET description = $1, homepage = $2, repository = $3, license = $4, latest_version = $5, updated_at = $6
			WHERE scope = $7 AND name = $8`,
			p.Description, p.Homepage, p.Repository, p.License, v.Version, now, p.Scope, p.Name,
		); err != nil {
			return err
		}
	}

	publishedAt := now
	if !v.PublishedAt.IsZero() {
		publishedAt = v.PublishedAt.UTC()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO versions (scope, name, version, checksum, manifest_json, published_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		v.Scope, v.Name, v.Version, v.Checksum, v.ManifestJSON, publishedAt,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) GetPlugin(ctx context.Context, scope, name string) (*store.Plugin, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT p.scope, p.name, p.description, p.homepage, p.repository, p.license, p.latest_version,
		       p.created_at, p.updated_at, COALESCE((SELECT SUM(download_count) FROM versions v WHERE v.scope = p.scope AND v.name = p.name), 0)
		FROM plugins p WHERE p.scope = $1 AND p.name = $2`, scope, name)
	var p store.Plugin
	if err := row.Scan(&p.Scope, &p.Name, &p.Description, &p.Homepage, &p.Repository, &p.License,
		&p.LatestVersion, &p.CreatedAt, &p.UpdatedAt, &p.TotalDownloads); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	p.CreatedAt = p.CreatedAt.UTC()
	p.UpdatedAt = p.UpdatedAt.UTC()

	rows, err := s.db.QueryContext(ctx, `SELECT keyword FROM keywords WHERE scope = $1 AND name = $2`, scope, name)
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
		FROM versions WHERE scope = $1 AND name = $2 AND version = $3`, scope, name, version)
	var v store.Version
	if err := row.Scan(&v.Scope, &v.Name, &v.Version, &v.Checksum, &v.ManifestJSON, &v.PublishedAt, &v.DownloadCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	v.PublishedAt = v.PublishedAt.UTC()
	return &v, nil
}

func (s *Store) ListVersions(ctx context.Context, scope, name string) ([]store.Version, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, name, version, checksum, manifest_json, published_at, download_count
		FROM versions WHERE scope = $1 AND name = $2 ORDER BY published_at ASC`, scope, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Version
	for rows.Next() {
		var v store.Version
		if err := rows.Scan(&v.Scope, &v.Name, &v.Version, &v.Checksum, &v.ManifestJSON, &v.PublishedAt, &v.DownloadCount); err != nil {
			return nil, err
		}
		v.PublishedAt = v.PublishedAt.UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) IncrementDownloadCount(ctx context.Context, scope, name, version string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE versions SET download_count = download_count + 1 WHERE scope = $1 AND name = $2 AND version = $3`,
		scope, name, version)
	return err
}

func (s *Store) Search(ctx context.Context, query string) ([]store.Plugin, error) {
	query = strings.TrimSpace(query)

	var rows *sql.Rows
	var err error
	if query == "" {
		// No query: browse-all, same as the old LIKE '%%' behavior —
		// websearch_to_tsquery('') produces an empty tsquery that matches
		// nothing, so this has to stay a separate path rather than
		// falling through below.
		rows, err = s.db.QueryContext(ctx, `
			SELECT p.scope, p.name, p.description, p.homepage, p.repository, p.license,
			       p.latest_version, p.created_at, p.updated_at,
			       COALESCE((SELECT SUM(download_count) FROM versions v WHERE v.scope = p.scope AND v.name = p.name), 0)
			FROM plugins p
			ORDER BY p.updated_at DESC`)
	} else {
		like := "%" + strings.ToLower(query) + "%"
		// websearch_to_tsquery parses raw search-box syntax (quotes,
		// "-" for exclusion, implicit AND between words) the way a real
		// search engine's box would, and — unlike plain to_tsquery —
		// never errors on malformed input, so no extra sanitization of
		// user-supplied query is needed here.
		rows, err = s.db.QueryContext(ctx, `
			SELECT DISTINCT p.scope, p.name, p.description, p.homepage, p.repository, p.license,
			       p.latest_version, p.created_at, p.updated_at,
			       COALESCE((SELECT SUM(download_count) FROM versions v WHERE v.scope = p.scope AND v.name = p.name), 0)
			FROM plugins p
			LEFT JOIN keywords k ON k.scope = p.scope AND k.name = p.name
			WHERE p.search_vector @@ websearch_to_tsquery('simple', $1)
			   OR lower(k.keyword) LIKE $2
			ORDER BY p.updated_at DESC`, query, like)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []store.Plugin
	for rows.Next() {
		var p store.Plugin
		if err := rows.Scan(&p.Scope, &p.Name, &p.Description, &p.Homepage, &p.Repository, &p.License,
			&p.LatestVersion, &p.CreatedAt, &p.UpdatedAt, &p.TotalDownloads); err != nil {
			return nil, err
		}
		p.CreatedAt = p.CreatedAt.UTC()
		p.UpdatedAt = p.UpdatedAt.UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Keywords are fetched per-plugin, same as GetPlugin and the same
	// rationale as internal/store/sqlite's Search: small result sets, and
	// identical code shape across both backends beats a Postgres-only
	// string_agg (SQLite's group_concat isn't the same syntax).
	for i := range out {
		kwRows, err := s.db.QueryContext(ctx, `SELECT keyword FROM keywords WHERE scope = $1 AND name = $2`, out[i].Scope, out[i].Name)
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
		`INSERT INTO email_verifications (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		tokenHash, userID, expiresAt.UTC(),
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
	var expiresAt time.Time
	err = tx.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM email_verifications WHERE token_hash = $1`, tokenHash,
	).Scan(&userID, &expiresAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, store.ErrTokenInvalid
		}
		return 0, err
	}
	if time.Now().UTC().After(expiresAt) {
		return 0, store.ErrTokenInvalid
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM email_verifications WHERE token_hash = $1`, tokenHash); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET email_verified = TRUE WHERE id = $1`, userID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}

func (s *Store) CreatePasswordReset(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO password_resets (token_hash, user_id, expires_at, used_at) VALUES ($1, $2, $3, NULL)`,
		tokenHash, userID, expiresAt.UTC(),
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
	var expiresAt time.Time
	var usedAt sql.NullTime
	err = tx.QueryRowContext(ctx,
		`SELECT user_id, expires_at, used_at FROM password_resets WHERE token_hash = $1`, tokenHash,
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
	if time.Now().UTC().After(expiresAt) {
		return 0, store.ErrTokenInvalid
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE password_resets SET used_at = $1 WHERE token_hash = $2`,
		time.Now().UTC(), tokenHash,
	); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return userID, nil
}

func (s *Store) UpdatePassword(ctx context.Context, userID int64, newPasswordHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2`, newPasswordHash, userID)
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var _ store.MetadataStore = (*Store)(nil)
