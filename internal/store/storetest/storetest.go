// Package storetest is a shared behavioral conformance suite for
// store.MetadataStore implementations. internal/store/sqlite and
// internal/store/postgres both run the exact same test cases against it —
// not hand-copied, the same code — so "swap the backend" is guaranteed to
// mean "identical behavior," not "probably similar behavior."
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/pkulkarni/apreg/internal/store"
)

// RunConformanceSuite runs the full behavioral contract of
// store.MetadataStore against a freshly constructed store. newStore is
// called once per top-level sub-test with a fresh, empty backend.
func RunConformanceSuite(t *testing.T, newStore func(t *testing.T) store.MetadataStore) {
	t.Run("UsersAndTokens", func(t *testing.T) { testUsersAndTokens(t, newStore(t)) })
	t.Run("PublishAndResolve", func(t *testing.T) { testPublishAndResolve(t, newStore(t)) })
	t.Run("DownloadCount", func(t *testing.T) { testDownloadCount(t, newStore(t)) })
	t.Run("EmailVerification", func(t *testing.T) { testEmailVerification(t, newStore(t)) })
	t.Run("PasswordReset", func(t *testing.T) { testPasswordReset(t, newStore(t)) })
}

func testUsersAndTokens(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "alice", "alice@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.EmailVerified {
		t.Fatal("expected new user to start unverified")
	}

	if _, err := s.CreateUser(ctx, "alice", "other@example.com", "hashed"); err != store.ErrConflict {
		t.Fatalf("expected ErrConflict for duplicate username, got %v", err)
	}

	got, err := s.GetUserByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.ID != u.ID {
		t.Fatalf("id mismatch: %d vs %d", got.ID, u.ID)
	}

	if _, err := s.CreateToken(ctx, u.ID, "hashed-token", "laptop"); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	owner, err := s.GetUserByTokenHash(ctx, "hashed-token")
	if err != nil {
		t.Fatalf("GetUserByTokenHash: %v", err)
	}
	if owner.Username != "alice" {
		t.Fatalf("unexpected owner: %+v", owner)
	}

	if _, err := s.GetUserByTokenHash(ctx, "nope"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func testPublishAndResolve(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()

	p := store.NewPlugin{Scope: "alice", Name: "hello", Description: "desc", Keywords: []string{"test"}}
	v1 := store.NewVersion{Scope: "alice", Name: "hello", Version: "1.0.0", Checksum: "abc", ManifestJSON: "{}"}

	if err := s.UpsertPluginAndVersion(ctx, p, v1); err != nil {
		t.Fatalf("UpsertPluginAndVersion v1: %v", err)
	}
	if err := s.UpsertPluginAndVersion(ctx, p, v1); err != store.ErrConflict {
		t.Fatalf("expected ErrConflict republishing same version, got %v", err)
	}

	v2 := store.NewVersion{Scope: "alice", Name: "hello", Version: "1.1.0", Checksum: "def", ManifestJSON: "{}"}
	if err := s.UpsertPluginAndVersion(ctx, p, v2); err != nil {
		t.Fatalf("UpsertPluginAndVersion v2: %v", err)
	}

	plugin, err := s.GetPlugin(ctx, "alice", "hello")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if plugin.LatestVersion != "1.1.0" {
		t.Fatalf("expected latest_version 1.1.0, got %s", plugin.LatestVersion)
	}
	if len(plugin.Keywords) != 1 || plugin.Keywords[0] != "test" {
		t.Fatalf("unexpected keywords: %v", plugin.Keywords)
	}

	versions, err := s.ListVersions(ctx, "alice", "hello")
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(versions))
	}

	results, err := s.Search(ctx, "hello")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Name != "hello" {
		t.Fatalf("unexpected search results: %v", results)
	}

	if _, err := s.GetVersion(ctx, "alice", "hello", "9.9.9"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing version, got %v", err)
	}
}

func testDownloadCount(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()
	p := store.NewPlugin{Scope: "bob", Name: "tool"}
	v := store.NewVersion{Scope: "bob", Name: "tool", Version: "1.0.0", Checksum: "abc", ManifestJSON: "{}"}
	if err := s.UpsertPluginAndVersion(ctx, p, v); err != nil {
		t.Fatalf("UpsertPluginAndVersion: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := s.IncrementDownloadCount(ctx, "bob", "tool", "1.0.0"); err != nil {
			t.Fatalf("IncrementDownloadCount: %v", err)
		}
	}

	ver, err := s.GetVersion(ctx, "bob", "tool", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if ver.DownloadCount != 3 {
		t.Fatalf("expected download count 3, got %d", ver.DownloadCount)
	}

	plugin, err := s.GetPlugin(ctx, "bob", "tool")
	if err != nil {
		t.Fatalf("GetPlugin: %v", err)
	}
	if plugin.TotalDownloads != 3 {
		t.Fatalf("expected total downloads 3, got %d", plugin.TotalDownloads)
	}
}

func testEmailVerification(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "carol", "carol@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := s.CreateEmailVerification(ctx, u.ID, "tok-valid", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreateEmailVerification: %v", err)
	}

	userID, err := s.ConsumeEmailVerification(ctx, "tok-valid")
	if err != nil {
		t.Fatalf("ConsumeEmailVerification: %v", err)
	}
	if userID != u.ID {
		t.Fatalf("expected userID %d, got %d", u.ID, userID)
	}

	got, err := s.GetUserByUsername(ctx, "carol")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if !got.EmailVerified {
		t.Fatal("expected user to be marked verified")
	}

	// Reusing a consumed token must fail — it was deleted on first use.
	if _, err := s.ConsumeEmailVerification(ctx, "tok-valid"); err != store.ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid reusing a consumed token, got %v", err)
	}

	if err := s.CreateEmailVerification(ctx, u.ID, "tok-expired", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CreateEmailVerification (expired): %v", err)
	}
	if _, err := s.ConsumeEmailVerification(ctx, "tok-expired"); err != store.ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid for expired token, got %v", err)
	}

	if _, err := s.ConsumeEmailVerification(ctx, "never-existed"); err != store.ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid for unknown token, got %v", err)
	}
}

func testPasswordReset(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "dave", "dave@example.com", "old-hash")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	if err := s.CreatePasswordReset(ctx, u.ID, "reset-valid", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("CreatePasswordReset: %v", err)
	}

	userID, err := s.ConsumePasswordReset(ctx, "reset-valid")
	if err != nil {
		t.Fatalf("ConsumePasswordReset: %v", err)
	}
	if userID != u.ID {
		t.Fatalf("expected userID %d, got %d", u.ID, userID)
	}

	if err := s.UpdatePassword(ctx, userID, "new-hash"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	got, err := s.GetUserByUsername(ctx, "dave")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if got.PasswordHash != "new-hash" {
		t.Fatalf("expected password hash to be updated, got %q", got.PasswordHash)
	}

	// A used token must not be reusable, even before expiry.
	if _, err := s.ConsumePasswordReset(ctx, "reset-valid"); err != store.ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid reusing a consumed reset token, got %v", err)
	}

	if err := s.CreatePasswordReset(ctx, u.ID, "reset-expired", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("CreatePasswordReset (expired): %v", err)
	}
	if _, err := s.ConsumePasswordReset(ctx, "reset-expired"); err != store.ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid for expired reset token, got %v", err)
	}
}
