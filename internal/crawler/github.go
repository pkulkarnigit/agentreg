package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/pkulkarni/apreg/internal/pack"
)

// ParseRepoURL parses a GitHub repository or repository-subdirectory URL
// into its owner, repo, ref (branch/tag — empty if not specified in the
// URL), and subpath (empty if the plugin lives at the repo root).
// Supports the two shapes the awesome-list uses:
//
//	https://github.com/{owner}/{repo}
//	https://github.com/{owner}/{repo}/tree/{ref}/{subpath...}
func ParseRepoURL(rawURL string) (owner, repo, ref, subpath string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", "", "", fmt.Errorf("parse URL: %w", err)
	}
	if u.Host != "github.com" {
		return "", "", "", "", fmt.Errorf("not a github.com URL: %s", rawURL)
	}

	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", "", "", fmt.Errorf("cannot parse owner/repo from %s", rawURL)
	}
	owner = parts[0]
	repo = strings.TrimSuffix(parts[1], ".git")

	if len(parts) >= 4 && parts[2] == "tree" {
		ref = parts[3]
		subpath = strings.Join(parts[4:], "/")
	}
	return owner, repo, ref, subpath, nil
}

// ResolveDefaultBranch looks up owner/repo's default branch via the GitHub
// REST API (unauthenticated; fine for the modest call volume a single
// crawl run makes since most entries specify their own ref via /tree/).
func ResolveDefaultBranch(ctx context.Context, client *http.Client, owner, repo string) (string, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API %s: HTTP %d", apiURL, resp.StatusCode)
	}

	var body struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("decode GitHub API response: %w", err)
	}
	if body.DefaultBranch == "" {
		return "", fmt.Errorf("GitHub API returned no default_branch for %s/%s", owner, repo)
	}
	return body.DefaultBranch, nil
}

const (
	// maxTarballDownloadBytes caps the compressed download per repo —
	// third-party content, so this is deliberately generous but bounded.
	maxTarballDownloadBytes int64 = 200 << 20
	// maxExtractedBytes caps decompressed output per repo (see
	// pack.ExtractReader's decompression-bomb protection).
	maxExtractedBytes int64 = 512 << 20
)

// FetchRepo downloads owner/repo@ref as a tarball from GitHub's codeload
// service and extracts it into destDir (which must already exist),
// stripping the archive's synthetic "{owner}-{repo}-{sha}/" root
// directory. Reuses pack's extraction core for the same path-traversal and
// decompression-bomb protections used elsewhere, in lenient mode
// (unsupported entry types like symlinks are skipped, not fatal — real
// third-party repos legitimately contain them).
func FetchRepo(ctx context.Context, client *http.Client, owner, repo, ref, destDir string) error {
	tarballURL := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/%s", owner, repo, ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tarballURL, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", tarballURL, resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, maxTarballDownloadBytes+1)
	return pack.ExtractReader(limited, destDir, pack.ExtractOptions{
		MaxBytes:             maxExtractedBytes,
		StripComponents:      1,
		SkipUnsupportedTypes: true,
	})
}
