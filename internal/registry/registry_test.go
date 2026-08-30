package registry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/sqlite"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	meta, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { meta.Close() })
	blob, err := fsblob.Open(t.TempDir())
	if err != nil {
		t.Fatalf("fsblob.Open: %v", err)
	}
	return New(meta, blob)
}

func testBundle(t *testing.T, name, version string) *manifest.Bundle {
	t.Helper()
	dir := t.TempDir()
	pluginJSON := `{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "` + name + `",
		"version": "` + version + `",
		"description": "test plugin"
	}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "skills", "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := manifest.ValidateDir(dir)
	if err != nil {
		t.Fatalf("ValidateDir: %v", err)
	}
	return b
}

func TestPublishAndResolve(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	b := testBundle(t, "hello", "1.0.0")

	v, err := r.Publish(ctx, PublishInput{
		Scope: "alice", Name: "hello", Version: "1.0.0",
		Tarball: strings.NewReader("fake-tarball-bytes"),
		Bundle:  b, RequestorU: "alice",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if v.Checksum == "" {
		t.Fatal("expected checksum to be set")
	}

	resolved, err := r.ResolveVersion(ctx, "alice", "hello", "latest")
	if err != nil {
		t.Fatalf("ResolveVersion(latest): %v", err)
	}
	if resolved.Version != "1.0.0" {
		t.Fatalf("expected latest to resolve to 1.0.0, got %s", resolved.Version)
	}

	rc, err := r.OpenTarball(ctx, "alice", "hello", "1.0.0")
	if err != nil {
		t.Fatalf("OpenTarball: %v", err)
	}
	rc.Close()
}

func TestPublish_WrongScopeForbidden(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	b := testBundle(t, "hello", "1.0.0")

	_, err := r.Publish(ctx, PublishInput{
		Scope: "bob", Name: "hello", Version: "1.0.0",
		Tarball: strings.NewReader("x"),
		Bundle:  b, RequestorU: "alice", // alice's token, but publishing under bob's scope
	})
	if err != ErrForbidden {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestPublish_NameMismatch(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	b := testBundle(t, "hello", "1.0.0")

	_, err := r.Publish(ctx, PublishInput{
		Scope: "alice", Name: "different-name", Version: "1.0.0",
		Tarball: strings.NewReader("x"),
		Bundle:  b, RequestorU: "alice",
	})
	if err == nil {
		t.Fatal("expected error for manifest/URL name mismatch")
	}
}

func TestPublish_ImmutableVersion(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	b := testBundle(t, "hello", "1.0.0")

	in := PublishInput{
		Scope: "alice", Name: "hello", Version: "1.0.0",
		Tarball: strings.NewReader("x"),
		Bundle:  b, RequestorU: "alice",
	}
	if _, err := r.Publish(ctx, in); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	in.Tarball = strings.NewReader("x")
	if _, err := r.Publish(ctx, in); err != ErrConflict {
		t.Fatalf("expected ErrConflict republishing same version, got %v", err)
	}
}

func TestPublish_InvalidSemver(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	b := testBundle(t, "hello", "not-a-version")

	_, err := r.Publish(ctx, PublishInput{
		Scope: "alice", Name: "hello", Version: "not-a-version",
		Tarball: strings.NewReader("x"),
		Bundle:  b, RequestorU: "alice",
	})
	if err == nil {
		t.Fatal("expected error for non-semver version")
	}
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	b := testBundle(t, "hello", "1.0.0")
	if _, err := r.Publish(ctx, PublishInput{
		Scope: "alice", Name: "hello", Version: "1.0.0",
		Tarball: strings.NewReader("x"), Bundle: b, RequestorU: "alice",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	results, err := r.Search(ctx, "hello")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}
