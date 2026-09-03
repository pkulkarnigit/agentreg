// Command crawler discovers publicly known Agent Plugins from the
// awesome-agent-plugins directory on GitHub, fetches and validates each
// one against internal/manifest (the same rules KrateAI enforces at
// publish time), and writes the results as a JSON catalog. With -publish,
// it also mirrors every valid entry into a running KrateAI registry
// under one dedicated account's scope (see internal/crawler.PublishConfig)
// — otherwise it's discovery/reporting only.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pkulkarni/apreg/internal/crawler"
	"github.com/pkulkarni/apreg/internal/logging"
)

func main() {
	out := flag.String("out", "catalog/agent-plugins-catalog.json", "path to write the JSON catalog to")
	sourceURL := flag.String("source", crawler.AwesomeListURL, "URL of the awesome-list markdown to crawl")
	publish := flag.Bool("publish", false, "also mirror every valid entry into a running KrateAI registry")
	registryURL := flag.String("registry", "", "registry URL to mirror into (required with -publish)")
	scope := flag.String("scope", "github-mirror", "the mirror account's username to publish under (required with -publish)")
	flag.Parse()

	logger := logging.New(os.Getenv("KRATE_LOG_LEVEL"), os.Stderr)
	logger.Debug("config resolved", "out", *out, "source", *sourceURL, "publish", *publish, "registry", *registryURL, "scope", *scope)

	var publishCfg *crawler.PublishConfig
	if *publish {
		token := os.Getenv("KRATE_MIRROR_TOKEN")
		if *registryURL == "" || *scope == "" || token == "" {
			logger.Error("-publish requires -registry, -scope, and KRATE_MIRROR_TOKEN (an API token for that account) to be set")
			os.Exit(1)
		}
		publishCfg = &crawler.PublishConfig{RegistryURL: *registryURL, Scope: *scope, Token: token}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	markdown, err := fetchText(ctx, *sourceURL)
	if err != nil {
		logger.Error("failed to fetch awesome-list", "url", *sourceURL, "error", err)
		os.Exit(1)
	}

	entries := crawler.ParseAwesomeList(markdown)
	if len(entries) == 0 {
		logger.Error("parsed zero entries — the awesome-list's format may have changed")
		os.Exit(1)
	}
	logger.Info("parsed entries", "count", len(entries))

	results := crawler.Crawl(ctx, logger, entries, publishCfg)

	cat := crawler.Catalog{
		GeneratedAt: time.Now().UTC(),
		Source:      *sourceURL,
		Results:     results,
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		logger.Error("failed to create output directory", "error", err)
		os.Exit(1)
	}
	f, err := os.Create(*out)
	if err != nil {
		logger.Error("failed to create catalog file", "path", *out, "error", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	encErr := enc.Encode(cat)
	f.Close()
	if encErr != nil {
		logger.Error("failed to write catalog", "error", encErr)
		os.Exit(1)
	}

	valid, invalid, published := 0, 0, 0
	for _, r := range results {
		if r.Valid {
			valid++
		} else {
			invalid++
		}
		if r.Published {
			published++
		}
	}
	if publishCfg != nil {
		fmt.Printf("Catalog written to %s: %d entries (%d valid, %d invalid/unreachable, %d mirrored to %s/@%s)\n",
			*out, len(results), valid, invalid, published, *registryURL, *scope)
	} else {
		fmt.Printf("Catalog written to %s: %d entries (%d valid, %d invalid/unreachable)\n", *out, len(results), valid, invalid)
	}
}

func fetchText(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	return string(b), err
}
