package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/pack"
)

func cmdPublish(args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.Token == "" || cfg.Registry == "" {
		return fmt.Errorf("not logged in — run `apreg login --registry <url>` first")
	}

	b, err := manifest.ValidateDir(dir)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	tmpFile, err := os.CreateTemp("", "apreg-publish-*.tar.gz")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	checksum, err := pack.Dir(dir, tmpPath)
	if err != nil {
		return fmt.Errorf("pack failed: %w", err)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer f.Close()

	url := fmt.Sprintf("%s/v1/plugins/%s/%s/%s", cfg.Registry, cfg.Username, b.Plugin.Name, b.Plugin.Version)
	req, err := http.NewRequest(http.MethodPut, url, f)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var ae apiError
		_ = decodeJSONBody(resp, &ae)
		if ae.Error != "" {
			return fmt.Errorf("publish failed: %s (HTTP %d)", ae.Error, resp.StatusCode)
		}
		return fmt.Errorf("publish failed: HTTP %d", resp.StatusCode)
	}

	fmt.Printf("Published @%s/%s@%s (sha256:%s)\n", cfg.Username, b.Plugin.Name, b.Plugin.Version, checksum)
	fmt.Printf("View it: %s/@%s/%s\n", cfg.Registry, cfg.Username, b.Plugin.Name)
	return nil
}
