package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pkulkarni/apreg/internal/store"
	"github.com/pkulkarni/apreg/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.RunConformanceSuite(t, func(t *testing.T) store.MetadataStore {
		s, err := Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

func TestBackup(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	if _, err := s.CreateUser(context.Background(), "alice", "alice@example.com", "hashed"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	restored, err := Open(backupPath)
	if err != nil {
		t.Fatalf("Open(backup): %v", err)
	}
	defer restored.Close()

	u, err := restored.GetUserByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername on restored backup: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Fatalf("unexpected restored user: %+v", u)
	}
}
