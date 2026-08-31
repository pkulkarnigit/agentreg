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
	t.Run("PublishedAtOverride", func(t *testing.T) { testPublishedAtOverride(t, newStore(t)) })
	t.Run("UpdateVersionPublishedAt", func(t *testing.T) { testUpdateVersionPublishedAt(t, newStore(t)) })
	t.Run("DownloadCount", func(t *testing.T) { testDownloadCount(t, newStore(t)) })
	t.Run("SearchIncludesDownloadCount", func(t *testing.T) { testSearchIncludesDownloadCount(t, newStore(t)) })
	t.Run("SearchEmptyQueryReturnsAll", func(t *testing.T) { testSearchEmptyQueryReturnsAll(t, newStore(t)) })
	t.Run("SearchMatchesWholeWords", func(t *testing.T) { testSearchMatchesWholeWords(t, newStore(t)) })
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
	// Regression coverage: Search used to select different columns than
	// GetPlugin and silently omit Keywords/TotalDownloads for every
	// result — every plugin card on the homepage showed "0 downloads"
	// and no keyword tags regardless of the real values.
	if len(results[0].Keywords) != 1 || results[0].Keywords[0] != "test" {
		t.Fatalf("Search result missing keywords: %v", results[0].Keywords)
	}

	if _, err := s.GetVersion(ctx, "alice", "hello", "9.9.9"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing version, got %v", err)
	}
}

// testPublishedAtOverride covers mirrored content's real reason for
// existing: a version's recorded publish date should reflect when it
// actually shipped upstream, not the moment a crawler happened to copy it
// in — so an explicit NewVersion.PublishedAt must be honored exactly, and
// the zero value (the ordinary, non-mirrored publish path) must still fall
// back to "now," not some zero-time artifact.
func testPublishedAtOverride(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()

	upstream := time.Date(2019, 3, 14, 9, 26, 53, 0, time.UTC)
	p1 := store.NewPlugin{Scope: "carol", Name: "vintage-plugin"}
	v1 := store.NewVersion{
		Scope: "carol", Name: "vintage-plugin", Version: "1.0.0",
		Checksum: "abc", ManifestJSON: "{}", PublishedAt: upstream,
	}
	if err := s.UpsertPluginAndVersion(ctx, p1, v1); err != nil {
		t.Fatalf("UpsertPluginAndVersion with explicit PublishedAt: %v", err)
	}
	got, err := s.GetVersion(ctx, "carol", "vintage-plugin", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if !got.PublishedAt.Equal(upstream) {
		t.Fatalf("expected published_at %s, got %s", upstream, got.PublishedAt)
	}

	before := time.Now()
	p2 := store.NewPlugin{Scope: "carol", Name: "fresh-plugin"}
	v2 := store.NewVersion{Scope: "carol", Name: "fresh-plugin", Version: "1.0.0", Checksum: "def", ManifestJSON: "{}"}
	if err := s.UpsertPluginAndVersion(ctx, p2, v2); err != nil {
		t.Fatalf("UpsertPluginAndVersion with zero-value PublishedAt: %v", err)
	}
	after := time.Now()
	got2, err := s.GetVersion(ctx, "carol", "fresh-plugin", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if got2.PublishedAt.Before(before.Add(-time.Second)) || got2.PublishedAt.After(after.Add(time.Second)) {
		t.Fatalf("expected published_at near now (%s..%s), got %s", before, after, got2.PublishedAt)
	}
}

// testUpdateVersionPublishedAt covers the admin backfill capability's
// storage layer: it must actually change the recorded date on an existing
// version (the one deliberate exception to immutability), leave every
// other field on that version untouched, and fail clearly against a
// version that doesn't exist rather than silently doing nothing.
func testUpdateVersionPublishedAt(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()

	p := store.NewPlugin{Scope: "dave", Name: "backfill-me"}
	v := store.NewVersion{
		Scope: "dave", Name: "backfill-me", Version: "1.0.0",
		Checksum: "original-checksum", ManifestJSON: `{"name":"backfill-me"}`,
	}
	if err := s.UpsertPluginAndVersion(ctx, p, v); err != nil {
		t.Fatalf("UpsertPluginAndVersion: %v", err)
	}

	corrected := time.Date(2019, 3, 14, 9, 26, 53, 0, time.UTC)
	if err := s.UpdateVersionPublishedAt(ctx, "dave", "backfill-me", "1.0.0", corrected); err != nil {
		t.Fatalf("UpdateVersionPublishedAt: %v", err)
	}

	got, err := s.GetVersion(ctx, "dave", "backfill-me", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if !got.PublishedAt.Equal(corrected) {
		t.Fatalf("expected published_at %s, got %s", corrected, got.PublishedAt)
	}
	// Nothing else about the version moved.
	if got.Checksum != "original-checksum" {
		t.Fatalf("expected checksum to stay untouched, got %q", got.Checksum)
	}

	if err := s.UpdateVersionPublishedAt(ctx, "dave", "backfill-me", "9.9.9", corrected); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound for a version that doesn't exist, got %v", err)
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

// testSearchIncludesDownloadCount is a regression test: Search's query
// used to select different columns than GetPlugin and never included the
// download-count aggregate, so every plugin returned by Search (i.e.
// every card on the homepage) silently showed 0 downloads no matter how
// many real downloads it had.
func testSearchIncludesDownloadCount(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()
	p := store.NewPlugin{Scope: "carol", Name: "widget"}
	v := store.NewVersion{Scope: "carol", Name: "widget", Version: "1.0.0", Checksum: "abc", ManifestJSON: "{}"}
	if err := s.UpsertPluginAndVersion(ctx, p, v); err != nil {
		t.Fatalf("UpsertPluginAndVersion: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := s.IncrementDownloadCount(ctx, "carol", "widget", "1.0.0"); err != nil {
			t.Fatalf("IncrementDownloadCount: %v", err)
		}
	}

	results, err := s.Search(ctx, "widget")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(results))
	}
	if results[0].TotalDownloads != 5 {
		t.Fatalf("Search result has stale download count: got %d, want 5", results[0].TotalDownloads)
	}
}

func testSearchEmptyQueryReturnsAll(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()
	for _, name := range []string{"one", "two"} {
		p := store.NewPlugin{Scope: "dave", Name: name}
		v := store.NewVersion{Scope: "dave", Name: name, Version: "1.0.0", Checksum: "abc", ManifestJSON: "{}"}
		if err := s.UpsertPluginAndVersion(ctx, p, v); err != nil {
			t.Fatalf("UpsertPluginAndVersion %s: %v", name, err)
		}
	}

	results, err := s.Search(ctx, "")
	if err != nil {
		t.Fatalf("Search(\"\"): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected empty query to return both plugins, got %d: %v", len(results), results)
	}

	results, err = s.Search(ctx, "   ")
	if err != nil {
		t.Fatalf("Search(whitespace): %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected whitespace-only query to return both plugins, got %d", len(results))
	}
}

// testSearchMatchesWholeWords documents the switch from substring LIKE
// matching to full-text search (SQLite FTS5 / Postgres tsvector) on both
// backends: a query has to match a whole indexed word, not an arbitrary
// substring within one. This is a deliberate behavior change (also what
// makes the index usable at all), not a regression, so it's pinned here
// rather than left implicit.
func testSearchMatchesWholeWords(t *testing.T, s store.MetadataStore) {
	ctx := context.Background()
	p := store.NewPlugin{Scope: "erin", Name: "automation-tool", Description: "a tool for automating workflows"}
	v := store.NewVersion{Scope: "erin", Name: "automation-tool", Version: "1.0.0", Checksum: "abc", ManifestJSON: "{}"}
	if err := s.UpsertPluginAndVersion(ctx, p, v); err != nil {
		t.Fatalf("UpsertPluginAndVersion: %v", err)
	}

	results, err := s.Search(ctx, "workflows")
	if err != nil {
		t.Fatalf("Search(whole word): %v", err)
	}
	if len(results) != 1 || results[0].Name != "automation-tool" {
		t.Fatalf("expected whole-word query to match, got %v", results)
	}

	results, err = s.Search(ctx, "flow")
	if err != nil {
		t.Fatalf("Search(substring): %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected a bare substring not to match a whole-word index, got %v", results)
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
