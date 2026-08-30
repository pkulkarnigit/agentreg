package crawler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkulkarni/apreg/internal/manifest"
)

// Result is one catalog entry: what the awesome-list claimed, plus what we
// actually found by fetching and validating it ourselves.
type Result struct {
	Entry

	Owner, Repo, Ref, Subpath string

	Valid           bool
	PluginName      string   `json:",omitempty"`
	PluginVersion   string   `json:",omitempty"`
	Skills          []string `json:",omitempty"`
	HasMCP          bool
	ValidationError string `json:",omitempty"`
	FetchError      string `json:",omitempty"`
}

// Catalog is the persisted output of a crawl run.
type Catalog struct {
	GeneratedAt time.Time `json:"generated_at"`
	Source      string    `json:"source"`
	Results     []Result  `json:"results"`
}

// Crawl fetches and validates every entry, returning results in the same
// order as entries. Repos referenced by more than one entry (common — the
// list has several large collections with many plugins each) are only
// downloaded once via repoCache, keyed by owner/repo/ref.
func Crawl(ctx context.Context, logger *slog.Logger, entries []Entry) []Result {
	if logger == nil {
		logger = slog.Default()
	}
	client := &http.Client{Timeout: 60 * time.Second}

	workDir, err := os.MkdirTemp("", "apreg-crawler-*")
	if err != nil {
		logger.Error("could not create work dir", "error", err)
		return nil
	}
	defer os.RemoveAll(workDir)

	type cacheEntry struct {
		dir string
		err error
	}
	repoCache := make(map[string]cacheEntry)

	results := make([]Result, len(entries))
	for i, e := range entries {
		r := Result{Entry: e}

		owner, repo, ref, subpath, err := ParseRepoURL(e.RepoURL)
		if err != nil {
			r.FetchError = err.Error()
			results[i] = r
			logger.Warn("skipping entry with unparseable repo URL", "name", e.Name, "url", e.RepoURL, "error", err)
			continue
		}
		r.Owner, r.Repo, r.Subpath = owner, repo, subpath

		if ref == "" {
			ref, err = ResolveDefaultBranch(ctx, client, owner, repo)
			if err != nil {
				r.FetchError = fmt.Sprintf("resolve default branch: %v", err)
				results[i] = r
				logger.Warn("could not resolve default branch", "name", e.Name, "owner", owner, "repo", repo, "error", err)
				continue
			}
		}
		r.Ref = ref

		cacheKey := owner + "/" + repo + "@" + ref
		cached, ok := repoCache[cacheKey]
		if !ok {
			dir := filepath.Join(workDir, fmt.Sprintf("repo-%d", len(repoCache)))
			if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
				cached = cacheEntry{err: mkErr}
			} else {
				logger.Info("fetching", "owner", owner, "repo", repo, "ref", ref)
				fetchErr := FetchRepo(ctx, client, owner, repo, ref, dir)
				cached = cacheEntry{dir: dir, err: fetchErr}
			}
			repoCache[cacheKey] = cached
		}
		if cached.err != nil {
			r.FetchError = sanitizePath(cached.err.Error(), workDir)
			results[i] = r
			logger.Warn("fetch failed", "name", e.Name, "owner", owner, "repo", repo, "ref", ref, "error", cached.err)
			continue
		}

		pluginDir := cached.dir
		if subpath != "" {
			pluginDir = filepath.Join(cached.dir, filepath.FromSlash(path.Clean(subpath)))
		}

		bundle, err := manifest.ValidateDir(pluginDir)
		if err != nil {
			r.ValidationError = sanitizePath(err.Error(), workDir)
			results[i] = r
			continue
		}

		r.Valid = true
		r.PluginName = bundle.Plugin.Name
		r.PluginVersion = bundle.Plugin.Version
		r.Skills = bundle.Skills
		r.HasMCP = bundle.HasMCP
		results[i] = r
	}

	return results
}

// sanitizePath strips the crawler's local temp-dir prefix from an error
// message so recorded errors read as repo-relative paths, not a leftover
// local filesystem detail from whichever machine ran the crawl — this
// ends up on a public catalog page.
func sanitizePath(msg, workDir string) string {
	return strings.ReplaceAll(msg, workDir+string(filepath.Separator), "")
}
