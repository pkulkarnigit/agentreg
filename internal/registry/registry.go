// Package registry holds all business rules for AgentReg: publishing,
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

	"golang.org/x/mod/semver"

	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/store"
)

var (
	ErrForbidden    = errors.New("forbidden: token does not own this scope")
	ErrNotFound     = store.ErrNotFound
	ErrConflict     = store.ErrConflict
	ErrInvalidInput = errors.New("invalid input")
)

type Registry struct {
	meta store.MetadataStore
	blob store.BlobStore
}

func New(meta store.MetadataStore, blob store.BlobStore) *Registry {
	return &Registry{meta: meta, blob: blob}
}

// PublishInput is everything needed to publish one version of a plugin.
type PublishInput struct {
	Scope       string // path segment; must equal the authenticated user's username
	Name        string // must equal manifest's own Name
	Version     string // must equal manifest's own Version
	Tarball     io.Reader
	Bundle      *manifest.Bundle // already validated by manifest.ValidateDir
	RequestorID int64            // authenticated user's ID
	RequestorU  string           // authenticated user's username
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
