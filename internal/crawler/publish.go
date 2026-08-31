package crawler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/pkulkarni/apreg/internal/pack"
)

// PublishConfig, when passed to Crawl, turns the discovery report into an
// actual mirror: every valid entry is also packed and published into a
// running KrateAI registry, all under one dedicated account's scope —
// never under an arbitrary/spoofed scope, since the server independently
// verifies Token's owner equals Scope on every publish, the same
// authorization check a normal `krate publish` goes through. This keeps
// mirrored content clearly attributed to "the mirror account", never
// impersonating the original author.
type PublishConfig struct {
	RegistryURL string
	Scope       string // must equal Token's owning username
	Token       string
}

// errAlreadyPublished signals a 409 from the registry: this exact
// scope/name/version was already mirrored on a previous run. Not a
// failure — versions are immutable by design, so re-running the crawler
// against unchanged upstream content is expected to hit this every time.
var errAlreadyPublished = errors.New("already published")

func publishToRegistry(ctx context.Context, client *http.Client, cfg PublishConfig, pluginDir, name, version string) error {
	tmpFile, err := os.CreateTemp("", "krate-mirror-*.tar.gz")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if _, err := pack.Dir(pluginDir, tmpPath); err != nil {
		return fmt.Errorf("pack: %w", err)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer f.Close()

	url := fmt.Sprintf("%s/v1/plugins/%s/%s/%s", strings.TrimRight(cfg.RegistryURL, "/"), cfg.Scope, name, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, f)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return errAlreadyPublished
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("publish failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
