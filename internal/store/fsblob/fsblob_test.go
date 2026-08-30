package fsblob

import (
	"context"
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

func TestOpenMissing(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s.Open(context.Background(), "alice", "nope", "1.0.0"); err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
