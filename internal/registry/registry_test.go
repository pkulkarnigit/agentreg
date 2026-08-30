package registry

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/store"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/sqlite"
)

// fakeSender captures messages instead of delivering them, so tests can
// pull the verification/reset token out of what would have been sent.
type fakeSender struct {
	to, subject, body string
	calls             int
}

func (f *fakeSender) Send(_ context.Context, to, subject, body string) error {
	f.to, f.subject, f.body = to, subject, body
	f.calls++
	return nil
}

var tokenInBodyRE = regexp.MustCompile(`(?:verify-email|--token) (\S+)`)

func extractToken(t *testing.T, body string) string {
	t.Helper()
	m := tokenInBodyRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("could not find token in message body: %q", body)
	}
	return m[1]
}

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
		Tarball: strings.NewReader("original bytes"),
		Bundle:  b, RequestorU: "alice",
	}
	first, err := r.Publish(ctx, in)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}

	// Deliberately DIFFERENT content on the "duplicate" — this is the
	// shape a real duplicate publish takes (e.g. the crawler's idempotent
	// re-runs re-pack a fresh tarball with new mtimes each time, so the
	// bytes differ even though it's logically "the same" plugin). Using
	// identical bytes here would let a storage-overwrite bug pass
	// undetected, since the checksum wouldn't visibly change.
	in.Tarball = strings.NewReader("different bytes from a re-packed tarball")
	if _, err := r.Publish(ctx, in); err != ErrConflict {
		t.Fatalf("expected ErrConflict republishing same version, got %v", err)
	}

	// The rejected duplicate must not have touched the stored blob: what
	// downloads now must still be exactly the first publish's bytes,
	// under the same checksum recorded at that first publish.
	resolved, err := r.ResolveVersion(ctx, "alice", "hello", "1.0.0")
	if err != nil {
		t.Fatalf("ResolveVersion: %v", err)
	}
	if resolved.Checksum != first.Checksum {
		t.Fatalf("recorded checksum changed after a rejected duplicate publish: %s -> %s", first.Checksum, resolved.Checksum)
	}
	rc, err := r.OpenTarball(ctx, "alice", "hello", "1.0.0")
	if err != nil {
		t.Fatalf("OpenTarball: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original bytes" {
		t.Fatalf("stored blob was overwritten by the rejected duplicate publish: got %q", got)
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

func TestDownloadCount(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	b := testBundle(t, "hello", "1.0.0")
	if _, err := r.Publish(ctx, PublishInput{
		Scope: "alice", Name: "hello", Version: "1.0.0",
		Tarball: strings.NewReader("x"), Bundle: b, RequestorU: "alice",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := r.IncrementDownloadCount(ctx, "alice", "hello", "1.0.0"); err != nil {
			t.Fatalf("IncrementDownloadCount: %v", err)
		}
	}

	v, err := r.ResolveVersion(ctx, "alice", "hello", "1.0.0")
	if err != nil {
		t.Fatalf("ResolveVersion: %v", err)
	}
	if v.DownloadCount != 2 {
		t.Fatalf("expected download count 2, got %d", v.DownloadCount)
	}
}

func TestEmailVerificationFlow(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	sender := &fakeSender{}
	r.SetSender(sender)

	u, err := r.Signup(ctx, "alice", "alice@example.com", "hashed")
	if err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if err := r.RequestEmailVerification(ctx, u); err != nil {
		t.Fatalf("RequestEmailVerification: %v", err)
	}
	if sender.calls != 1 || sender.to != "alice@example.com" {
		t.Fatalf("unexpected sender state: %+v", sender)
	}
	token := extractToken(t, sender.body)

	if err := r.ConfirmEmailVerification(ctx, token); err != nil {
		t.Fatalf("ConfirmEmailVerification: %v", err)
	}

	got, err := r.UserByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if !got.EmailVerified {
		t.Fatal("expected user to be marked verified")
	}

	// Reusing the token must fail.
	if err := r.ConfirmEmailVerification(ctx, token); err != store.ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid reusing a consumed token, got %v", err)
	}

	// A forged/unknown token must fail the same way.
	if err := r.ConfirmEmailVerification(ctx, "totally-made-up-token"); err != store.ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid for a forged token, got %v", err)
	}
}

func TestPasswordResetFlow(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	sender := &fakeSender{}
	r.SetSender(sender)

	if _, err := r.Signup(ctx, "bob", "bob@example.com", "old-hash"); err != nil {
		t.Fatalf("Signup: %v", err)
	}

	if err := r.RequestPasswordReset(ctx, "bob"); err != nil {
		t.Fatalf("RequestPasswordReset: %v", err)
	}
	if sender.calls != 1 || sender.to != "bob@example.com" {
		t.Fatalf("unexpected sender state: %+v", sender)
	}
	token := extractToken(t, sender.body)

	if err := r.ConfirmPasswordReset(ctx, token, "new-hash"); err != nil {
		t.Fatalf("ConfirmPasswordReset: %v", err)
	}

	got, err := r.UserByUsername(ctx, "bob")
	if err != nil {
		t.Fatalf("UserByUsername: %v", err)
	}
	if got.PasswordHash != "new-hash" {
		t.Fatalf("expected password hash updated, got %q", got.PasswordHash)
	}

	if err := r.ConfirmPasswordReset(ctx, token, "another-hash"); err != store.ErrTokenInvalid {
		t.Fatalf("expected ErrTokenInvalid reusing a consumed reset token, got %v", err)
	}
}

func TestPasswordReset_UnknownUsernameDoesNotError(t *testing.T) {
	ctx := context.Background()
	r := newTestRegistry(t)
	sender := &fakeSender{}
	r.SetSender(sender)

	// Must not error and must not reveal (via error or send) that the
	// username doesn't exist.
	if err := r.RequestPasswordReset(ctx, "nobody-by-this-name"); err != nil {
		t.Fatalf("expected nil error for unknown username, got %v", err)
	}
	if sender.calls != 0 {
		t.Fatalf("expected no message sent for an unknown username, got %d calls", sender.calls)
	}
}
