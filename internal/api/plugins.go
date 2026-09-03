package api

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/pkulkarni/apreg/internal/api/middleware"
	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/pack"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store"
)

const maxTarballBytes = 64 << 20 // 64MiB, generous for a plugin bundle

// handlePublish accepts a raw tar.gz body (Content-Type: application/gzip),
// re-validates it server-side (defense in depth on top of client-side
// `krate validate`), and stores it.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	scope := r.PathValue("scope")
	name := r.PathValue("name")
	version := r.PathValue("version")
	slog.Debug("publish requested", "requestor", user.Username, "scope", scope, "name", name, "version", version)

	var publishedAt time.Time
	if raw := r.URL.Query().Get("published_at"); raw != "" {
		var err error
		publishedAt, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "published_at must be RFC3339, e.g. 2024-01-15T09:00:00Z")
			return
		}
	}

	tmpDir, err := os.MkdirTemp("", "krate-publish-*")
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	defer os.RemoveAll(tmpDir)

	tarballPath := filepath.Join(tmpDir, "upload.tar.gz")
	tarballFile, err := os.Create(tarballPath)
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	n, err := io.Copy(tarballFile, io.LimitReader(r.Body, maxTarballBytes+1))
	tarballFile.Close()
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed reading upload body")
		return
	}
	if n > maxTarballBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "tarball exceeds 64MiB limit")
		return
	}
	slog.Debug("tarball received", "scope", scope, "name", name, "version", version, "bytes", n)

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		writeInternalError(w, r, err)
		return
	}
	if err := pack.Unpack(tarballPath, extractDir); err != nil {
		writeError(w, http.StatusBadRequest, "invalid tar.gz archive: "+err.Error())
		return
	}

	bundle, err := manifest.ValidateDir(extractDir)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	tarballFile, err = os.Open(tarballPath)
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	defer tarballFile.Close()

	v, err := s.reg.Publish(r.Context(), registry.PublishInput{
		Scope:       scope,
		Name:        name,
		Version:     version,
		Tarball:     tarballFile,
		Bundle:      bundle,
		RequestorU:  user.Username,
		PublishedAt: publishedAt,
	})
	if err != nil {
		writePublishError(w, r, err)
		return
	}
	slog.Info("published", "scope", v.Scope, "name", v.Name, "version", v.Version)

	writeJSON(w, http.StatusCreated, map[string]any{
		"scope":        v.Scope,
		"name":         v.Name,
		"version":      v.Version,
		"checksum":     v.Checksum,
		"published_at": v.PublishedAt,
	})
}

func writePublishError(w http.ResponseWriter, r *http.Request, err error) {
	switch err {
	case registry.ErrForbidden:
		writeError(w, http.StatusForbidden, err.Error())
	case registry.ErrConflict:
		writeError(w, http.StatusConflict, "this scope/name/version has already been published; versions are immutable")
	default:
		if _, ok := isInvalidInput(err); ok {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeInternalError(w, r, err)
	}
}

// handleAdminSetPublishedAt backfills an already-published version's
// recorded date — the one deliberate hole in "versions are immutable,"
// reserved for correcting mirrored content published before its real
// upstream date was known (or before this endpoint existed at all).
// Nothing else about the version — checksum, manifest — can be touched
// through this or any other endpoint. Gated by RequireAdmin, not the
// normal token-owns-scope rule handlePublish uses: an admin fixing the
// mirror account's dates isn't the mirror account.
func (s *Server) handleAdminSetPublishedAt(w http.ResponseWriter, r *http.Request) {
	scope := r.PathValue("scope")
	name := r.PathValue("name")
	version := r.PathValue("version")

	var body struct {
		PublishedAt time.Time `json:"published_at"`
	}
	if !decodeJSONBody(w, r, &body) {
		return
	}

	if err := s.reg.AdminSetPublishedAt(r.Context(), scope, name, version, body.PublishedAt); err != nil {
		switch {
		case err == registry.ErrNotFound:
			writeError(w, http.StatusNotFound, "no such scope/name/version")
		default:
			if _, ok := isInvalidInput(err); ok {
				writeError(w, http.StatusUnprocessableEntity, err.Error())
				return
			}
			writeInternalError(w, r, err)
		}
		return
	}
	slog.Info("published_at backfilled", "scope", scope, "name", name, "version", version, "published_at", body.PublishedAt)

	v, err := s.reg.ResolveVersion(r.Context(), scope, name, version)
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope":        v.Scope,
		"name":         v.Name,
		"version":      v.Version,
		"published_at": v.PublishedAt,
	})
}

func isInvalidInput(err error) (error, bool) {
	if err == nil {
		return nil, false
	}
	for e := err; e != nil; {
		if e == registry.ErrInvalidInput {
			return e, true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			break
		}
		e = u.Unwrap()
	}
	return nil, false
}

func (s *Server) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	scope, name := r.PathValue("scope"), r.PathValue("name")
	p, err := s.reg.GetPlugin(r.Context(), scope, name)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	versions, err := s.reg.ListVersions(r.Context(), scope, name)
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	versionStrings := make([]string, len(versions))
	for i, v := range versions {
		versionStrings[i] = v.Version
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scope":           p.Scope,
		"name":            p.Name,
		"description":     p.Description,
		"homepage":        p.Homepage,
		"repository":      p.Repository,
		"license":         p.License,
		"keywords":        p.Keywords,
		"latest_version":  p.LatestVersion,
		"versions":        versionStrings,
		"total_downloads": p.TotalDownloads,
		"created_at":      p.CreatedAt,
		"updated_at":      p.UpdatedAt,
	})
}

func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	scope, name, version := r.PathValue("scope"), r.PathValue("name"), r.PathValue("version")
	v, err := s.reg.ResolveVersion(r.Context(), scope, name, version)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope":          v.Scope,
		"name":           v.Name,
		"version":        v.Version,
		"checksum":       v.Checksum,
		"manifest":       rawJSON(v.ManifestJSON),
		"published_at":   v.PublishedAt,
		"download_count": v.DownloadCount,
		"download_url":   "/v1/plugins/" + scope + "/" + name + "/" + v.Version + "/download",
	})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	scope, name, version := r.PathValue("scope"), r.PathValue("name"), r.PathValue("version")
	resolved, err := s.reg.ResolveVersion(r.Context(), scope, name, version)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	rc, err := s.reg.OpenTarball(r.Context(), scope, name, resolved.Version)
	if err != nil {
		writeStoreError(w, r, err)
		return
	}
	defer rc.Close()

	if err := s.reg.IncrementDownloadCount(r.Context(), scope, name, resolved.Version); err != nil {
		slog.Warn("failed to increment download count", "scope", scope, "name", name, "version", resolved.Version, "error", err)
	}
	slog.Debug("download served", "scope", scope, "name", name, "version", resolved.Version)

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Checksum-Sha256", resolved.Checksum)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+"-"+resolved.Version+`.tar.gz"`)
	io.Copy(w, rc)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	results, err := s.reg.Search(r.Context(), q)
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	slog.Debug("search executed", "query", q, "results", len(results))
	out := make([]map[string]any, len(results))
	for i, p := range results {
		out[i] = map[string]any{
			"scope":          p.Scope,
			"name":           p.Name,
			"description":    p.Description,
			"latest_version": p.LatestVersion,
			"keywords":       p.Keywords,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func writeStoreError(w http.ResponseWriter, r *http.Request, err error) {
	if err == store.ErrNotFound {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeInternalError(w, r, err)
}
