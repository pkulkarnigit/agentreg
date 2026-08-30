package fsblob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/pkulkarni/apreg/internal/store"
)

func TestPutOpen(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	content := "hello world"
	checksum, size, err := s.Put(ctx, "alice", "hello", "1.0.0", strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if size != int64(len(content)) {
		t.Fatalf("size mismatch: got %d want %d", size, len(content))
	}
	if checksum == "" {
		t.Fatal("expected non-empty checksum")
	}

	r, err := s.Open(ctx, "alice", "hello", "1.0.0")
	if err != nil {
		t.Fatalf("Open blob: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("content mismatch: got %q", got)
	}
}

// TestPut_RefusesToOverwriteExistingBlob is a regression test: Put used to
// use os.Rename, which silently clobbers an existing file. A second Put
// for the same scope/name/version (which the registry layer is supposed
// to reject before ever calling Put — but this is the hard backstop) must
// leave the originally stored bytes untouched and report ErrConflict,
// never overwrite them with different content under the same recorded
// checksum.
func TestPut_RefusesToOverwriteExistingBlob(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	firstChecksum, _, err := s.Put(ctx, "alice", "hello", "1.0.0", strings.NewReader("original content"))
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}

	_, _, err = s.Put(ctx, "alice", "hello", "1.0.0", strings.NewReader("DIFFERENT content, e.g. a re-packed tarball with new mtimes"))
	if err != store.ErrConflict {
		t.Fatalf("expected ErrConflict on duplicate Put, got %v", err)
	}

	// The original bytes (and their checksum) must be exactly what a
	// caller who reads them back gets — never silently replaced.
	r, err := s.Open(ctx, "alice", "hello", "1.0.0")
	if err != nil {
		t.Fatalf("Open blob: %v", err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original content" {
		t.Fatalf("blob content changed after a rejected duplicate Put: got %q", got)
	}

	h := sha256.Sum256(got)
	if hex.EncodeToString(h[:]) != firstChecksum {
		t.Fatal("stored content no longer matches the checksum recorded at first Put")
	}
}

func TestOpenMissing(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Open(context.Background(), "alice", "nope", "1.0.0"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
