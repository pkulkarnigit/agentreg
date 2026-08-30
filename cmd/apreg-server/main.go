// Command apreg-server runs the AgentReg HTTP server: the /v1 REST API
// (internal/api) and the read-only browsing UI (internal/web), backed by a
// SQLite (default) or Postgres (APREG_DB_DRIVER=postgres) metadata store
// and local-filesystem blob storage.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pkulkarni/apreg/internal/api"
	"github.com/pkulkarni/apreg/internal/api/middleware"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/postgres"
	"github.com/pkulkarni/apreg/internal/store/sqlite"
	"github.com/pkulkarni/apreg/internal/web"
)

const shutdownTimeout = 15 * time.Second

func main() {
	backupPath := flag.String("backup", "", "write a consistent database backup to this path and exit (sqlite driver only; use pg_dump for postgres)")
	flag.Parse()

	dataDir := envOr("APREG_DATA_DIR", "./data")
	addr := envOr("APREG_ADDR", ":8080")
	driver := envOr("APREG_DB_DRIVER", "sqlite")
	dsn := os.Getenv("APREG_DB_DSN")

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", dataDir, err)
	}

	meta, err := openMetadataStore(driver, dsn, dataDir, *backupPath)
	if err != nil {
		log.Fatal(err)
	}
	if meta == nil {
		// Backup mode: openMetadataStore already did the work and closed
		// the store; nothing left to do.
		return
	}
	defer meta.Close()

	blob, err := fsblob.Open(filepath.Join(dataDir, "blobs"))
	if err != nil {
		log.Fatalf("open blob store: %v", err)
	}

	reg := registry.New(meta, blob)
	// No real mail provider is wired up (see internal/notify's doc
	// comment): reg defaults to logging verification/reset links instead
	// of emailing them. That's fine for local/Docker use, but anyone with
	// read access to these logs can take over any account via those
	// links — this MUST be swapped for a real notify.Sender
	// (reg.SetSender(...)) before this server is ever exposed to
	// untrusted users or the public internet.
	log.Print("WARNING: no email provider configured — account verification/password-reset links are being written to this log, not emailed. Do not expose this server to untrusted users without configuring a real notify.Sender first.")
	apiHandler := api.NewHandler(reg)

	mux := http.NewServeMux()
	mux.Handle("/v1/", apiHandler)
	mux.Handle("/healthz", apiHandler)
	mux.Handle("/", web.NewHandler(reg))

	logger := slog.Default()
	handler := middleware.Logging(logger, mux)

	srv := &http.Server{Addr: addr, Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("apreg-server listening on %s (driver: %s, data dir: %s)", addr, driver, dataDir)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	case <-ctx.Done():
		log.Print("shutting down (signal received)...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown failed: %v", err)
		} else {
			log.Print("shutdown complete")
		}
	}
}

// openMetadataStore opens the configured backend. In backup mode (sqlite
// only) it performs the backup, closes the store, and returns (nil, nil)
// to signal the caller there's nothing further to do.
func openMetadataStore(driver, dsn, dataDir, backupPath string) (store.MetadataStore, error) {
	switch driver {
	case "sqlite":
		path := dsn
		if path == "" {
			path = filepath.Join(dataDir, "apreg.db")
		}
		s, err := sqlite.Open(path)
		if err != nil {
			return nil, err
		}
		if backupPath != "" {
			err := s.Backup(backupPath)
			s.Close()
			if err != nil {
				return nil, err
			}
			log.Printf("backup written to %s", backupPath)
			return nil, nil
		}
		return s, nil

	case "postgres":
		if backupPath != "" {
			return nil, errors.New("-backup only supports the sqlite driver; use pg_dump for Postgres backups")
		}
		if dsn == "" {
			return nil, errors.New("APREG_DB_DSN is required when APREG_DB_DRIVER=postgres")
		}
		return postgres.Open(dsn)

	default:
		return nil, errors.New("unknown APREG_DB_DRIVER " + driver + " (want \"sqlite\" or \"postgres\")")
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
