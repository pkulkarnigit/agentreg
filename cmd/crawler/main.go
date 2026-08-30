// Command crawler discovers publicly known Agent Plugins from the
// awesome-agent-plugins directory on GitHub, fetches and validates each
// one against internal/manifest (the same rules AgentReg enforces at
// publish time), and writes the results as a JSON catalog. This is a
// discovery/reporting tool only — it never publishes anything into a
// running AgentReg instance.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pkulkarni/apreg/internal/crawler"
)

func main() {
	out := flag.String("out", "catalog/agent-plugins-catalog.json", "path to write the JSON catalog to")
	sourceURL := flag.String("source", crawler.AwesomeListURL, "URL of the awesome-list markdown to crawl")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

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

	results := crawler.Crawl(ctx, logger, entries)

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

	valid, invalid := 0, 0
	for _, r := range results {
		if r.Valid {
			valid++
		} else {
			invalid++
		}
	}
	fmt.Printf("Catalog written to %s: %d entries (%d valid, %d invalid/unreachable)\n", *out, len(results), valid, invalid)
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
