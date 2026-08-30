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

	"github.com/redis/go-redis/v9"

	"github.com/pkulkarni/apreg/internal/api"
	"github.com/pkulkarni/apreg/internal/api/middleware"
	"github.com/pkulkarni/apreg/internal/auth"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/postgres"
	"github.com/pkulkarni/apreg/internal/store/s3blob"
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
	catalogPath := envOr("APREG_CATALOG_PATH", "catalog/agent-plugins-catalog.json")
	mirrorScope := envOr("APREG_MIRROR_SCOPE", "github-mirror")

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

	blobDriver := envOr("APREG_BLOB_DRIVER", "fs")
	blob, err := openBlobStore(blobDriver, dataDir)
	if err != nil {
		log.Fatal(err)
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

	apiOpts, redisAddr := apiOptions()
	apiHandler := api.NewHandler(reg, apiOpts...)

	mux := http.NewServeMux()
	mux.Handle("/v1/", apiHandler)
	mux.Handle("/healthz", apiHandler)
	mux.Handle("/", web.NewHandler(reg, catalogPath, mirrorScope))

	logger := slog.Default()
	handler := middleware.Logging(logger, mux)

	srv := &http.Server{Addr: addr, Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rateLimitBackend := "in-memory"
	if redisAddr != "" {
		rateLimitBackend = "redis (" + redisAddr + ")"
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("apreg-server listening on %s (db driver: %s, blob driver: %s, rate limiting: %s, data dir: %s)", addr, driver, blobDriver, rateLimitBackend, dataDir)
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

// openBlobStore opens the configured blob backend. "fs" (default) stores
// tarballs under dataDir/blobs on local disk, tying the server to one
// machine. "s3" stores them in an S3-compatible bucket instead — the swap
// that makes apreg-server safe to run as multiple replicas, since every
// instance can see every blob.
func openBlobStore(driver, dataDir string) (store.BlobStore, error) {
	switch driver {
	case "fs":
		return fsblob.Open(filepath.Join(dataDir, "blobs"))

	case "s3":
		bucket := os.Getenv("APREG_S3_BUCKET")
		if bucket == "" {
			return nil, errors.New("APREG_S3_BUCKET is required when APREG_BLOB_DRIVER=s3")
		}
		return s3blob.Open(context.Background(), s3blob.Config{
			Bucket:         bucket,
			Region:         os.Getenv("APREG_S3_REGION"),
			Endpoint:       os.Getenv("APREG_S3_ENDPOINT"),
			ForcePathStyle: os.Getenv("APREG_S3_FORCE_PATH_STYLE") == "true",
		})

	default:
		return nil, errors.New("unknown APREG_BLOB_DRIVER " + driver + " (want \"fs\" or \"s3\")")
	}
}

// apiOptions decides whether the API's rate limiting and login lockout run
// in-memory (single instance, the default) or against Redis (shared across
// replicas, opt-in via APREG_REDIS_ADDR). It reuses api's own tuning
// constants so the Redis-backed path behaves identically to the in-memory
// default it's replacing, just with shared state. Returns the redis addr
// too (empty when unused) purely so the caller can log which backend is
// active.
func apiOptions() ([]api.Option, string) {
	redisAddr := os.Getenv("APREG_REDIS_ADDR")
	if redisAddr == "" {
		return nil, ""
	}

	client := redis.NewClient(&redis.Options{Addr: redisAddr})

	opts := []api.Option{
		api.WithLockout(auth.NewRedisLockout(client, "login", api.LoginLockoutMaxFailures, api.LoginLockoutWindow)),
		api.WithLimiterFactory(func(name string, burst int, per time.Duration) middleware.Limiter {
			return middleware.NewRedisLimiter(client, name, burst, per)
		}),
	}
	return opts, redisAddr
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
