// Package store defines the persistence interfaces used by internal/registry.
// registry is the only package permitted to depend on this package; nothing
// in internal/api or internal/web should import a concrete implementation
// (internal/store/sqlite, internal/store/fsblob) directly. See the
// "Modularity & scaling" section of the project plan: swapping SQLite for
// Postgres, or local disk for S3, means adding a new file that implements
// these interfaces, not touching registry/api/web.
package store

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned by lookups that find nothing.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when an insert would violate a uniqueness rule
// (duplicate username/email, or republishing an existing version).
var ErrConflict = errors.New("conflict")

// ErrTokenInvalid is returned when consuming an email-verification or
// password-reset token that does not exist, has expired, or was already
// used. Deliberately a single sentinel rather than three distinct ones:
// callers shouldn't distinguish "expired" from "already used" from "never
// existed" for a single-use random token — there's no legitimate reason a
// caller needs to tell those apart, and collapsing them avoids giving an
// attacker probing tokens any extra signal.
var ErrTokenInvalid = errors.New("token invalid, expired, or already used")

type User struct {
	ID            int64
	Username      string
	Email         string
	PasswordHash  string
	EmailVerified bool
	CreatedAt     time.Time
}

type Token struct {
	ID         int64
	UserID     int64
	Label      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

type Plugin struct {
	Scope          string
	Name           string
	Description    string
	Homepage       string
	Repository     string
	License        string
	LatestVersion  string
	Keywords       []string
	TotalDownloads int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type Version struct {
	Scope         string
	Name          string
	Version       string
	Checksum      string
	ManifestJSON  string
	PublishedAt   time.Time
	DownloadCount int64
}

// NewPlugin/NewVersion are the write-side inputs for publishing.
type NewPlugin struct {
	Scope       string
	Name        string
	Description string
	Homepage    string
	Repository  string
	License     string
	Keywords    []string
}

type NewVersion struct {
	Scope        string
	Name         string
	Version      string
	Checksum     string
	ManifestJSON string
}

// MetadataStore persists users, tokens, plugins, and versions. It does not
// know about tarball bytes — see BlobStore for that.
type MetadataStore interface {
	CreateUser(ctx context.Context, username, email, passwordHash string) (*User, error)
	GetUserByUsername(ctx context.Context, username string) (*User, error)

	CreateToken(ctx context.Context, userID int64, tokenHash, label string) (*Token, error)
	// GetUserByTokenHash resolves a bearer token to its owning user and
	// updates the token's last-used timestamp.
	GetUserByTokenHash(ctx context.Context, tokenHash string) (*User, error)

	// UpsertPluginAndVersion atomically creates the plugin row if absent,
	// inserts the new immutable version, and updates latest_version.
	// Returns ErrConflict if scope+name+version already exists.
	UpsertPluginAndVersion(ctx context.Context, p NewPlugin, v NewVersion) error

	GetPlugin(ctx context.Context, scope, name string) (*Plugin, error)
	GetVersion(ctx context.Context, scope, name, version string) (*Version, error)
	ListVersions(ctx context.Context, scope, name string) ([]Version, error)
	// Search does a simple substring match over scope/name/description/keywords.
	Search(ctx context.Context, query string) ([]Plugin, error)
	// IncrementDownloadCount is called once per successful tarball download.
	IncrementDownloadCount(ctx context.Context, scope, name, version string) error

	// CreateEmailVerification records a one-time token for confirming a
	// user's email address.
	CreateEmailVerification(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error
	// ConsumeEmailVerification atomically validates and deletes a
	// verification token, marks the owning user verified, and returns
	// their ID. Returns ErrTokenInvalid if the token is missing/expired.
	ConsumeEmailVerification(ctx context.Context, tokenHash string) (userID int64, err error)

	// CreatePasswordReset records a one-time token for resetting a user's
	// password.
	CreatePasswordReset(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error
	// ConsumePasswordReset atomically validates and marks-used a reset
	// token and returns the owning user's ID. Returns ErrTokenInvalid if
	// the token is missing, expired, or already used.
	ConsumePasswordReset(ctx context.Context, tokenHash string) (userID int64, err error)
	UpdatePassword(ctx context.Context, userID int64, newPasswordHash string) error

	Close() error
}

// BlobStore persists and serves plugin tarballs. Content is addressed by
// scope/name/version.
type BlobStore interface {
	// Put streams r to storage and returns the sha256 checksum (hex) and
	// byte size actually written.
	Put(ctx context.Context, scope, name, version string, r io.Reader) (checksum string, size int64, err error)
	// Open returns a reader for a previously stored blob.
	Open(ctx context.Context, scope, name, version string) (io.ReadCloser, error)
}
