// Package registry holds all business rules for KrateAI: publishing,
// version resolution, search, and publish authorization. It is the only
// package that talks to internal/store; internal/api and internal/web call
// only this package. See the project plan's "Modularity & scaling" section.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/mod/semver"

	"github.com/pkulkarni/apreg/internal/auth"
	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/notify"
	"github.com/pkulkarni/apreg/internal/store"
)

const (
	emailVerificationTTL = 24 * time.Hour
	passwordResetTTL     = 1 * time.Hour
)

var (
	ErrForbidden    = errors.New("forbidden: token does not own this scope")
	ErrNotFound     = store.ErrNotFound
	ErrConflict     = store.ErrConflict
	ErrInvalidInput = errors.New("invalid input")
)

type Registry struct {
	meta   store.MetadataStore
	blob   store.BlobStore
	sender notify.Sender
}

func New(meta store.MetadataStore, blob store.BlobStore) *Registry {
	return &Registry{meta: meta, blob: blob, sender: notify.LogSender{}}
}

// SetSender overrides the notification sender used for account-recovery
// emails (default: notify.LogSender, which just logs the link — see that
// package's doc comment). Real SMTP/SES delivery at actual deploy time is
// a new Sender implementation passed here, not a change to any caller.
func (r *Registry) SetSender(s notify.Sender) { r.sender = s }

// PublishInput is everything needed to publish one version of a plugin.
type PublishInput struct {
	Scope       string // path segment; must equal the authenticated user's username
	Name        string // must equal manifest's own Name
	Version     string // must equal manifest's own Version
	Tarball     io.Reader
	Bundle      *manifest.Bundle // already validated by manifest.ValidateDir
	RequestorID int64            // authenticated user's ID
	RequestorU  string           // authenticated user's username
	// PublishedAt, if set, records the version under this timestamp
	// instead of the current server time — for mirrored content, where
	// the meaningful date is when the plugin actually shipped upstream.
	// Self-reported, the same way a git commit's author date is: not
	// independently verified against anything, just trusted the way this
	// registry already trusts every other manifest field a publisher
	// provides. Rejected outright if it's in the future — that's not a
	// legitimate "upstream" date under any interpretation, just a
	// malformed or malicious one.
	PublishedAt time.Time
}

// Publish validates that the requesting user owns {scope}, that the
// manifest's own name/version match the URL path, stores the tarball, and
// records the version. Versions are immutable: republishing scope+name+
// version returns ErrConflict.
func (r *Registry) Publish(ctx context.Context, in PublishInput) (*store.Version, error) {
	if in.RequestorU != in.Scope {
		return nil, ErrForbidden
	}
	if in.Bundle.Plugin.Name != in.Name {
		return nil, fmt.Errorf("%w: plugin.json name %q does not match URL name %q", ErrInvalidInput, in.Bundle.Plugin.Name, in.Name)
	}
	if in.Bundle.Plugin.Version != in.Version {
		return nil, fmt.Errorf("%w: plugin.json version %q does not match URL version %q", ErrInvalidInput, in.Bundle.Plugin.Version, in.Version)
	}
	if !semver.IsValid("v" + in.Version) {
		return nil, fmt.Errorf("%w: version %q is not valid semver", ErrInvalidInput, in.Version)
	}
	if !in.PublishedAt.IsZero() && in.PublishedAt.After(time.Now()) {
		return nil, fmt.Errorf("%w: published_at %s is in the future", ErrInvalidInput, in.PublishedAt.Format(time.RFC3339))
	}

	// Check immutability BEFORE writing to blob storage. Versions are
	// immutable, so a duplicate publish (e.g. a retried request, or the
	// crawler's idempotent re-runs) must be rejected without touching the
	// stored blob — otherwise it would silently overwrite the file on
	// disk with a fresh (and not necessarily byte-identical — tarball
	// headers embed mtimes) copy, while the checksum recorded at the
	// *first* publish stays unchanged, corrupting every future download
	// of a version the caller was told already existed unchanged.
	if _, err := r.meta.GetVersion(ctx, in.Scope, in.Name, in.Version); err == nil {
		return nil, ErrConflict
	} else if err != store.ErrNotFound {
		return nil, err
	}

	checksum, _, err := r.blob.Put(ctx, in.Scope, in.Name, in.Version, in.Tarball)
	if err != nil {
		return nil, fmt.Errorf("store tarball: %w", err)
	}

	manifestBytes, err := json.Marshal(in.Bundle.Plugin)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	manifestJSON := string(manifestBytes)

	np := store.NewPlugin{
		Scope:       in.Scope,
		Name:        in.Name,
		Description: in.Bundle.Plugin.Description,
		Homepage:    in.Bundle.Plugin.Homepage,
		Repository:  in.Bundle.Plugin.Repository,
		License:     in.Bundle.Plugin.License,
		Keywords:    in.Bundle.Plugin.Keywords,
	}
	nv := store.NewVersion{
		Scope:        in.Scope,
		Name:         in.Name,
		Version:      in.Version,
		Checksum:     checksum,
		ManifestJSON: manifestJSON,
		PublishedAt:  in.PublishedAt,
	}

	if err := r.meta.UpsertPluginAndVersion(ctx, np, nv); err != nil {
		return nil, err
	}

	return r.meta.GetVersion(ctx, in.Scope, in.Name, in.Version)
}

func (r *Registry) GetPlugin(ctx context.Context, scope, name string) (*store.Plugin, error) {
	return r.meta.GetPlugin(ctx, scope, name)
}

func (r *Registry) ListVersions(ctx context.Context, scope, name string) ([]store.Version, error) {
	return r.meta.ListVersions(ctx, scope, name)
}

// ResolveVersion resolves "latest" to the plugin's latest_version, then
// looks up that concrete version.
func (r *Registry) ResolveVersion(ctx context.Context, scope, name, version string) (*store.Version, error) {
	if version == "" || version == "latest" {
		p, err := r.meta.GetPlugin(ctx, scope, name)
		if err != nil {
			return nil, err
		}
		version = p.LatestVersion
	}
	return r.meta.GetVersion(ctx, scope, name, version)
}

func (r *Registry) OpenTarball(ctx context.Context, scope, name, version string) (io.ReadCloser, error) {
	return r.blob.Open(ctx, scope, name, version)
}

func (r *Registry) Search(ctx context.Context, query string) ([]store.Plugin, error) {
	return r.meta.Search(ctx, query)
}

// Signup creates a new user account.
func (r *Registry) Signup(ctx context.Context, username, email, passwordHash string) (*store.User, error) {
	return r.meta.CreateUser(ctx, username, email, passwordHash)
}

func (r *Registry) UserByUsername(ctx context.Context, username string) (*store.User, error) {
	return r.meta.GetUserByUsername(ctx, username)
}

// IssueToken records a new API token for a user.
func (r *Registry) IssueToken(ctx context.Context, userID int64, tokenHash, label string) (*store.Token, error) {
	return r.meta.CreateToken(ctx, userID, tokenHash, label)
}

// Authenticate resolves a bearer token's sha256 hash to its owning user.
func (r *Registry) Authenticate(ctx context.Context, tokenHash string) (*store.User, error) {
	return r.meta.GetUserByTokenHash(ctx, tokenHash)
}

// IncrementDownloadCount is called once per successful tarball download.
func (r *Registry) IncrementDownloadCount(ctx context.Context, scope, name, version string) error {
	return r.meta.IncrementDownloadCount(ctx, scope, name, version)
}

// RequestEmailVerification issues a fresh verification token for user and
// delivers it via the configured Sender. Verification is advisory in v1 —
// an unverified account can still log in and publish; gating publish on
// verification is a one-line change once real email delivery exists.
func (r *Registry) RequestEmailVerification(ctx context.Context, user *store.User) error {
	token, err := auth.NewRandomToken()
	if err != nil {
		return err
	}
	if err := r.meta.CreateEmailVerification(ctx, user.ID, auth.HashToken(token), time.Now().Add(emailVerificationTTL)); err != nil {
		return err
	}
	body := fmt.Sprintf("Confirm your KrateAI account:\n\n  krate verify-email %s\n\nThis link expires in 24 hours.", token)
	return r.sender.Send(ctx, user.Email, "Verify your KrateAI account", body)
}

// ConfirmEmailVerification consumes a verification token, marking its
// owning user's email verified. Returns store.ErrTokenInvalid for a
// missing, expired, or already-used token.
func (r *Registry) ConfirmEmailVerification(ctx context.Context, token string) error {
	_, err := r.meta.ConsumeEmailVerification(ctx, auth.HashToken(token))
	return err
}

// RequestPasswordReset issues a fresh reset token for username (looking up
// their registered email to deliver it to) if the account exists.
// Deliberately returns nil rather than store.ErrNotFound for an unknown
// username — callers must not let this endpoint reveal which usernames
// are registered.
func (r *Registry) RequestPasswordReset(ctx context.Context, username string) error {
	user, err := r.meta.GetUserByUsername(ctx, username)
	if err != nil {
		if err == store.ErrNotFound {
			return nil
		}
		return err
	}
	token, err := auth.NewRandomToken()
	if err != nil {
		return err
	}
	if err := r.meta.CreatePasswordReset(ctx, user.ID, auth.HashToken(token), time.Now().Add(passwordResetTTL)); err != nil {
		return err
	}
	body := fmt.Sprintf("Reset your KrateAI password:\n\n  krate reset-password --token %s\n\nThis link expires in 1 hour. If you didn't request this, ignore this message.", token)
	return r.sender.Send(ctx, user.Email, "Reset your KrateAI password", body)
}

// ConfirmPasswordReset consumes a reset token and sets a new password
// hash. Returns store.ErrTokenInvalid for a missing, expired, or
// already-used token.
func (r *Registry) ConfirmPasswordReset(ctx context.Context, token, newPasswordHash string) error {
	userID, err := r.meta.ConsumePasswordReset(ctx, auth.HashToken(token))
	if err != nil {
		return err
	}
	return r.meta.UpdatePassword(ctx, userID, newPasswordHash)
}
