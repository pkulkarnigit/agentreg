// Package fsblob implements store.BlobStore on the local filesystem.
package fsblob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pkulkarni/apreg/internal/store"
)

type Store struct {
	root string
}

// Open ensures root exists and returns a Store rooted there.
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create blob root %s: %w", root, err)
	}
	return &Store{root: root}, nil
}

func (s *Store) path(scope, name, version string) string {
	return filepath.Join(s.root, scope, name, version+".tar.gz")
}

func (s *Store) Put(_ context.Context, scope, name, version string, r io.Reader) (string, int64, error) {
	target := s.path(scope, name, version)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", 0, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return "", 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		tmp.Close()
		return "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func (s *Store) Open(_ context.Context, scope, name, version string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(scope, name, version))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

var _ store.BlobStore = (*Store)(nil)
