// Command krate-server runs the KrateAI HTTP server: the /v1 REST API
// (internal/api) and the read-only browsing UI (internal/web), backed by a
// SQLite (default) or Postgres (KRATE_DB_DRIVER=postgres) metadata store
// and local-filesystem blob storage.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/pkulkarni/apreg/internal/api"
	"github.com/pkulkarni/apreg/internal/api/middleware"
	"github.com/pkulkarni/apreg/internal/auth"
	"github.com/pkulkarni/apreg/internal/logging"
	"github.com/pkulkarni/apreg/internal/notify"
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

	// Built and installed as the default logger before anything else runs,
	// so every slog.Info/Warn/Error/Debug call anywhere in this binary —
	// including the ones scattered through internal/api, internal/auth,
	// internal/notify that just call the package-level slog functions —
	// picks up the configured level and format automatically, with no
	// logger to thread through every function signature. KRATE_LOG_LEVEL
	// unset (or unrecognized) means "info": DEBUG and TRACE exist in the
	// code unconditionally, they just don't print until an operator asks
	// for them.
	logger := logging.New(os.Getenv("KRATE_LOG_LEVEL"), os.Stderr)
	slog.SetDefault(logger)

	dataDir := envOr("KRATE_DATA_DIR", "./data")
	addr := envOr("KRATE_ADDR", ":8080")
	driver := envOr("KRATE_DB_DRIVER", "sqlite")
	dsn := os.Getenv("KRATE_DB_DSN")
	catalogPath := envOr("KRATE_CATALOG_PATH", "catalog/agent-plugins-catalog.json")
	mirrorScope := envOr("KRATE_MIRROR_SCOPE", "github-mirror")
	logger.Debug("config resolved", "data_dir", dataDir, "addr", addr, "db_driver", driver, "catalog_path", catalogPath, "mirror_scope", mirrorScope)

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		logger.Error("create data dir", "dir", dataDir, "error", err)
		os.Exit(1)
	}

	meta, err := openMetadataStore(driver, dsn, dataDir, *backupPath)
	if err != nil {
		logger.Error("open metadata store", "driver", driver, "error", err)
		os.Exit(1)
	}
	if meta == nil {
		// Backup mode: openMetadataStore already did the work and closed
		// the store; nothing left to do.
		return
	}
	defer meta.Close()

	blobDriver := envOr("KRATE_BLOB_DRIVER", "fs")
	blob, err := openBlobStore(blobDriver, dataDir)
	if err != nil {
		logger.Error("open blob store", "driver", blobDriver, "error", err)
		os.Exit(1)
	}

	reg := registry.New(meta, blob)
	mailBackend, err := configureSender(reg)
	if err != nil {
		logger.Error("configure mail sender", "error", err)
		os.Exit(1)
	}

	apiOpts, redisAddr := apiOptions()
	apiHandler := api.NewHandler(reg, apiOpts...)

	mux := http.NewServeMux()
	mux.Handle("/v1/", apiHandler)
	mux.Handle("/healthz", apiHandler)
	mux.Handle("/", web.NewHandler(reg, catalogPath, mirrorScope))

	handler := middleware.Logging(logger, mux)

	srv := &http.Server{Addr: addr, Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rateLimitBackend := "in-memory"
	if redisAddr != "" {
		rateLimitBackend = "redis (" + redisAddr + ")"
	}
	admins := "none"
	if raw := os.Getenv("KRATE_ADMIN_USERNAMES"); raw != "" {
		admins = raw
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("krate-server listening",
			"addr", addr, "db_driver", driver, "blob_driver", blobDriver,
			"rate_limiting", rateLimitBackend, "mail", mailBackend, "admins", admins, "data_dir", dataDir,
		)
		serverErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutting down (signal received)")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("graceful shutdown failed", "error", err)
		} else {
			logger.Info("shutdown complete")
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
			path = filepath.Join(dataDir, "krate.db")
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
			slog.Info("backup written", "path", backupPath)
			return nil, nil
		}
		return s, nil

	case "postgres":
		if backupPath != "" {
			return nil, errors.New("-backup only supports the sqlite driver; use pg_dump for Postgres backups")
		}
		if dsn == "" {
			return nil, errors.New("KRATE_DB_DSN is required when KRATE_DB_DRIVER=postgres")
		}
		return postgres.Open(dsn)

	default:
		return nil, errors.New("unknown KRATE_DB_DRIVER " + driver + " (want \"sqlite\" or \"postgres\")")
	}
}

// openBlobStore opens the configured blob backend. "fs" (default) stores
// tarballs under dataDir/blobs on local disk, tying the server to one
// machine. "s3" stores them in an S3-compatible bucket instead — the swap
// that makes krate-server safe to run as multiple replicas, since every
// instance can see every blob.
func openBlobStore(driver, dataDir string) (store.BlobStore, error) {
	switch driver {
	case "fs":
		return fsblob.Open(filepath.Join(dataDir, "blobs"))

	case "s3":
		bucket := os.Getenv("KRATE_S3_BUCKET")
		if bucket == "" {
			return nil, errors.New("KRATE_S3_BUCKET is required when KRATE_BLOB_DRIVER=s3")
		}
		return s3blob.Open(context.Background(), s3blob.Config{
			Bucket:         bucket,
			Region:         os.Getenv("KRATE_S3_REGION"),
			Endpoint:       os.Getenv("KRATE_S3_ENDPOINT"),
			ForcePathStyle: os.Getenv("KRATE_S3_FORCE_PATH_STYLE") == "true",
		})

	default:
		return nil, errors.New("unknown KRATE_BLOB_DRIVER " + driver + " (want \"fs\" or \"s3\")")
	}
}

// configureSender wires up real SMTP delivery for account verification and
// password-reset emails when KRATE_SMTP_HOST is set, otherwise leaves
// reg on its default notify.LogSender — which just writes the link to
// this process's log instead of emailing it. That's fine for local/Docker
// use, but anyone with read access to these logs can take over any
// account via those links, so this MUST be configured with real SMTP
// before this server is ever exposed to untrusted users or the public
// internet. Returns a short description for the startup log line.
func configureSender(reg *registry.Registry) (string, error) {
	host := os.Getenv("KRATE_SMTP_HOST")
	if host == "" {
		slog.Warn("no email provider configured — account verification/password-reset links are being written to this log, not emailed. Do not expose this server to untrusted users without setting KRATE_SMTP_HOST first.")
		return "none (logging links)", nil
	}

	port, err := strconv.Atoi(envOr("KRATE_SMTP_PORT", "587"))
	if err != nil {
		return "", fmt.Errorf("invalid KRATE_SMTP_PORT: %w", err)
	}
	username := os.Getenv("KRATE_SMTP_USERNAME")
	reg.SetSender(notify.SMTPSender{
		Host:     host,
		Port:     port,
		Username: username,
		Password: os.Getenv("KRATE_SMTP_PASSWORD"),
		From:     os.Getenv("KRATE_SMTP_FROM"),
	})
	// Username only — the password never gets logged at any level, on
	// purpose.
	slog.Debug("smtp sender configured", "host", host, "port", port, "username", username, "auth", username != "")
	return fmt.Sprintf("smtp (%s:%d)", host, port), nil
}

// apiOptions decides whether the API's rate limiting and login lockout run
// in-memory (single instance, the default) or against Redis (shared across
// replicas, opt-in via KRATE_REDIS_ADDR), and who (if anyone) can reach
// the admin-only endpoints (KRATE_ADMIN_USERNAMES, comma-separated —
// empty by default, meaning nobody). It reuses api's own tuning constants
// so the Redis-backed path behaves identically to the in-memory default
// it's replacing, just with shared state. Returns the redis addr too
// (empty when unused) purely so the caller can log which backend is
// active.
func apiOptions() ([]api.Option, string) {
	var opts []api.Option

	if raw := os.Getenv("KRATE_ADMIN_USERNAMES"); raw != "" {
		var admins []string
		for _, u := range strings.Split(raw, ",") {
			if u = strings.TrimSpace(u); u != "" {
				admins = append(admins, u)
			}
		}
		slog.Debug("admin usernames configured", "usernames", admins)
		opts = append(opts, api.WithAdminUsernames(admins))
	}

	redisAddr := os.Getenv("KRATE_REDIS_ADDR")
	if redisAddr == "" {
		return opts, ""
	}

	slog.Debug("rate limiting/lockout backed by redis", "addr", redisAddr)
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	opts = append(opts,
		api.WithLockout(auth.NewRedisLockout(client, "login", api.LoginLockoutMaxFailures, api.LoginLockoutWindow)),
		api.WithLimiterFactory(func(name string, burst int, per time.Duration) middleware.Limiter {
			return middleware.NewRedisLimiter(client, name, burst, per)
		}),
	)
	return opts, redisAddr
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
