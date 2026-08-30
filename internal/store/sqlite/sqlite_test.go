package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pkulkarni/apreg/internal/store"
)

func TestUsersAndTokens(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	u, err := s.CreateUser(ctx, "alice", "alice@example.com", "hashed")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
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

	tok, err := s.CreateToken(ctx, u.ID, "hashed-token", "laptop")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	_ = tok

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

func TestPublishAndResolve(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

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
