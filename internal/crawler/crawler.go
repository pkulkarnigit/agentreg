package crawler

import (
	"context"
	"errors"
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

	// Published is true once this exact version is confirmed live in the
	// mirror registry — either just published this run, or already there
	// from a previous run (re-running the crawler is idempotent: an
	// unchanged upstream version publishes once, ever). Only meaningful
	// when Crawl was called with a non-nil PublishConfig.
	Published    bool   `json:",omitempty"`
	PublishError string `json:",omitempty"`

	// UpstreamPublishedAt is when this version's content actually last
	// changed upstream (the commit that touched it), recorded as the
	// mirror's own published_at instead of the moment this crawl run
	// happened to copy it in. Only populated for entries that were newly
	// published this run — a version already mirrored on a previous run
	// skips the GitHub lookup entirely (see versionAlreadyPublished) since
	// its published_at was already set correctly back when it was first
	// published, and re-fetching would just spend GitHub API budget for
	// a value that's already recorded.
	UpstreamPublishedAt *time.Time `json:"upstream_published_at,omitempty"`
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
// downloaded once via repoCache, keyed by owner/repo/ref. If publish is
// non-nil, every valid entry is also published into that registry (see
// PublishConfig) before its temp files are cleaned up.
func Crawl(ctx context.Context, logger *slog.Logger, entries []Entry, publish *PublishConfig) []Result {
	if logger == nil {
		logger = slog.Default()
	}
	client := &http.Client{Timeout: 60 * time.Second}

	workDir, err := os.MkdirTemp("", "krate-crawler-*")
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

		if publish != nil {
			switch {
			case bundle.Plugin.Version == "":
				// Publishing requires an immutable, addressable version;
				// the spec allows plugin.json to omit one, so this is a
				// real (not exceptional) case, not a bug.
				r.PublishError = "cannot mirror: plugin.json has no version field"
			default:
				alreadyPublished, err := versionAlreadyPublished(ctx, client, *publish, bundle.Plugin.Name, bundle.Plugin.Version)
				switch {
				case err != nil:
					r.PublishError = sanitizePath(fmt.Sprintf("check existing publish: %v", err), workDir)
					logger.Warn("could not check existing publish", "name", e.Name, "plugin", bundle.Plugin.Name, "version", bundle.Plugin.Version, "error", err)
				case alreadyPublished:
					r.Published = true
				default:
					// Only spend a GitHub API call on the real upstream
					// date for versions actually being published this
					// run — see UpstreamPublishedAt's doc comment.
					commitDate, dateErr := LastCommitDate(ctx, client, owner, repo, ref, subpath)
					if dateErr != nil {
						r.PublishError = sanitizePath(fmt.Sprintf("resolve upstream publish date: %v", dateErr), workDir)
						logger.Warn("could not resolve upstream publish date", "name", e.Name, "plugin", bundle.Plugin.Name, "version", bundle.Plugin.Version, "error", dateErr)
						break
					}
					pubErr := publishToRegistry(ctx, client, *publish, pluginDir, bundle.Plugin.Name, bundle.Plugin.Version, commitDate)
					switch {
					case pubErr == nil:
						r.Published = true
						r.UpstreamPublishedAt = &commitDate
						logger.Info("published", "plugin", bundle.Plugin.Name, "version", bundle.Plugin.Version, "upstream_published_at", commitDate)
					case errors.Is(pubErr, errAlreadyPublished):
						// Raced with something else publishing it between
						// the check above and this call — rare, but the
						// pre-check isn't a lock, just an optimization.
						r.Published = true
					default:
						r.PublishError = sanitizePath(pubErr.Error(), workDir)
						logger.Warn("publish failed", "name", e.Name, "plugin", bundle.Plugin.Name, "version", bundle.Plugin.Version, "error", pubErr)
					}
				}
			}
		}

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
