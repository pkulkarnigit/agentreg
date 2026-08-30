package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pkulkarni/apreg/internal/pack"
)

func registryURL() (string, error) {
	cfg, err := loadConfig()
	if err != nil {
		return "", err
	}
	if cfg.Registry == "" {
		return "", fmt.Errorf("no registry configured — run `apreg login --registry <url>` first")
	}
	return cfg.Registry, nil
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	destDir := fs.String("dir", "", "target directory (default: agent_plugins/<name>)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: apreg install @scope/name[@version]")
	}
	scope, name, version, err := parseRef(fs.Arg(0))
	if err != nil {
		return err
	}
	if version == "" {
		version = "latest"
	}

	base, err := registryURL()
	if err != nil {
		return err
	}

	var meta struct {
		Version  string `json:"version"`
		Checksum string `json:"checksum"`
	}
	metaURL := fmt.Sprintf("%s/v1/plugins/%s/%s/%s", base, scope, name, version)
	if err := doJSON(http.MethodGet, metaURL, "", nil, &meta); err != nil {
		return fmt.Errorf("resolve %s: %w", fs.Arg(0), err)
	}

	dlURL := fmt.Sprintf("%s/v1/plugins/%s/%s/%s/download", base, scope, name, meta.Version)
	resp, err := http.Get(dlURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "apreg-install-*.tar.gz")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	checksum, err := copyWithChecksum(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		return err
	}
	if checksum != meta.Checksum {
		return fmt.Errorf("checksum mismatch: registry says %s, downloaded content hashes to %s", meta.Checksum, checksum)
	}

	dest := *destDir
	if dest == "" {
		dest = filepath.Join("agent_plugins", name)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if err := pack.Unpack(tmpPath, dest); err != nil {
		return err
	}

	fmt.Printf("Installed @%s/%s@%s into %s (sha256:%s verified)\n", scope, name, meta.Version, dest, checksum)
	return nil
}

func cmdSearch(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: apreg search <query>")
	}
	base, err := registryURL()
	if err != nil {
		return err
	}

	var resp struct {
		Results []struct {
			Scope         string   `json:"scope"`
			Name          string   `json:"name"`
			Description   string   `json:"description"`
			LatestVersion string   `json:"latest_version"`
			Keywords      []string `json:"keywords"`
		} `json:"results"`
	}
	url := fmt.Sprintf("%s/v1/search?q=%s", base, args[0])
	if err := doJSON(http.MethodGet, url, "", nil, &resp); err != nil {
		return err
	}

	if len(resp.Results) == 0 {
		fmt.Println("No results.")
		return nil
	}
	for _, r := range resp.Results {
		fmt.Printf("@%s/%s@%s — %s\n", r.Scope, r.Name, r.LatestVersion, r.Description)
	}
	return nil
}

func cmdView(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: apreg view @scope/name[@version]")
	}
	scope, name, version, err := parseRef(args[0])
	if err != nil {
		return err
	}
	if version == "" {
		version = "latest"
	}

	base, err := registryURL()
	if err != nil {
		return err
	}

	var meta map[string]any
	url := fmt.Sprintf("%s/v1/plugins/%s/%s/%s", base, scope, name, version)
	if err := doJSON(http.MethodGet, url, "", nil, &meta); err != nil {
		return err
	}

	enc, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(enc))
	return nil
}

func copyWithChecksum(dst io.Writer, src io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(dst, h), src); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
