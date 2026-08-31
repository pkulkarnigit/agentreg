package postgres

import (
	"fmt"
	"os"
	"testing"

	"github.com/pkulkarni/apreg/internal/store"
	"github.com/pkulkarni/apreg/internal/store/storetest"
)

// TestConformance runs the exact same behavioral suite internal/store/sqlite
// runs, against real Postgres, proving both backends satisfy
// store.MetadataStore identically. Requires KRATE_TEST_POSTGRES_DSN
// (pointing at an empty, disposable database); skips cleanly without it so
// the rest of the suite still runs in environments with no Postgres
// reachable (e.g. no Docker daemon).
func TestConformance(t *testing.T) {
	baseDSN := os.Getenv("KRATE_TEST_POSTGRES_DSN")
	if baseDSN == "" {
		t.Skip("KRATE_TEST_POSTGRES_DSN not set; skipping Postgres conformance suite")
	}

	n := 0
	storetest.RunConformanceSuite(t, func(t *testing.T) store.MetadataStore {
		n++
		s, err := Open(baseDSN)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		// database/sql pools connections, and `SET search_path` is
		// per-connection session state — without pinning to one
		// connection, a later query could land on a different pooled
		// connection that's still defaulted to the "public" schema.
		// Pinning to 1 keeps every query in this test on the same
		// session, the same way internal/store/sqlite does (for a
		// different reason: SQLite's single-writer model).
		s.db.SetMaxOpenConns(1)

		// Each sub-test gets isolated data via a unique schema, so the
		// shared suite's fixed usernames/tokens don't collide across runs.
		schema := fmt.Sprintf("krate_test_%d", n)
		if _, err := s.db.Exec(fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, schema)); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		if _, err := s.db.Exec(fmt.Sprintf(`SET search_path TO %s`, schema)); err != nil {
			t.Fatalf("set search_path: %v", err)
		}
		if _, err := s.db.Exec(schemaSQL); err != nil {
			t.Fatalf("apply schema: %v", err)
		}
		if _, err := s.db.Exec(migrateSearchVectorSQL); err != nil {
			t.Fatalf("migrate search index: %v", err)
		}
		t.Cleanup(func() {
			s.db.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, schema))
			s.Close()
		})
		return s
	})
}
