// Command apreg-server runs the AgentReg HTTP server: the /v1 REST API
// (internal/api) and the read-only browsing UI (internal/web), backed by a
// local SQLite database and local-filesystem blob storage.
package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pkulkarni/apreg/internal/api"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/sqlite"
	"github.com/pkulkarni/apreg/internal/web"
)

func main() {
	dataDir := envOr("APREG_DATA_DIR", "./data")
	addr := envOr("APREG_ADDR", ":8080")

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", dataDir, err)
	}

	meta, err := sqlite.Open(filepath.Join(dataDir, "apreg.db"))
	if err != nil {
		log.Fatalf("open metadata store: %v", err)
	}
	defer meta.Close()

	blob, err := fsblob.Open(filepath.Join(dataDir, "blobs"))
	if err != nil {
		log.Fatalf("open blob store: %v", err)
	}

	reg := registry.New(meta, blob)
	apiHandler := api.NewHandler(reg)

	mux := http.NewServeMux()
	mux.Handle("/v1/", apiHandler)
	mux.Handle("/healthz", apiHandler)
	mux.Handle("/", web.NewHandler(reg))

	log.Printf("apreg-server listening on %s (data dir: %s)", addr, dataDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
