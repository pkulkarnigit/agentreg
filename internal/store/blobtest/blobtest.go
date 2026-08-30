// Package blobtest is a shared behavioral conformance suite for
// store.BlobStore implementations — the blob-storage counterpart to
// internal/store/storetest. internal/store/fsblob and internal/store/s3blob
// both run the exact same cases, so "swap the backend" is a guarantee, not
// a hope.
package blobtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"testing"

	"github.com/pkulkarni/apreg/internal/store"
)

// RunConformanceSuite runs the full behavioral contract of
// store.BlobStore against a freshly constructed store. newStore is called
// once per top-level sub-test with a fresh, empty backend.
func RunConformanceSuite(t *testing.T, newStore func(t *testing.T) store.BlobStore) {
	t.Run("PutOpen", func(t *testing.T) { testPutOpen(t, newStore(t)) })
	t.Run("RefusesToOverwriteExistingBlob", func(t *testing.T) { testRefusesToOverwriteExistingBlob(t, newStore(t)) })
	t.Run("OpenMissing", func(t *testing.T) { testOpenMissing(t, newStore(t)) })
}

func testPutOpen(t *testing.T, s store.BlobStore) {
	ctx := context.Background()

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
	h := sha256.Sum256([]byte(content))
	if checksum != hex.EncodeToString(h[:]) {
		t.Fatalf("checksum mismatch: got %s want %s", checksum, hex.EncodeToString(h[:]))
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

// testRefusesToOverwriteExistingBlob is a regression test born from a real
// bug: an early fsblob.Put used os.Rename, which silently clobbers an
// existing file. A second Put for the same scope/name/version (which the
// registry layer is supposed to reject before ever calling Put — this is
// the hard backstop) must leave the originally stored bytes untouched and
// report ErrConflict, never overwrite them under the same recorded
// checksum.
func testRefusesToOverwriteExistingBlob(t *testing.T, s store.BlobStore) {
	ctx := context.Background()

	firstChecksum, _, err := s.Put(ctx, "bob", "widget", "1.0.0", strings.NewReader("original content"))
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}

	_, _, err = s.Put(ctx, "bob", "widget", "1.0.0", strings.NewReader("DIFFERENT content, e.g. a re-packed tarball with new mtimes"))
	if err != store.ErrConflict {
		t.Fatalf("expected ErrConflict on duplicate Put, got %v", err)
	}

	r, err := s.Open(ctx, "bob", "widget", "1.0.0")
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

func testOpenMissing(t *testing.T, s store.BlobStore) {
	if _, err := s.Open(context.Background(), "nobody", "nothing", "1.0.0"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
