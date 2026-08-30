package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/pkulkarni/apreg/internal/api/middleware"
	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/pack"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store"
)

const maxTarballBytes = 64 << 20 // 64MiB, generous for a plugin bundle

// handlePublish accepts a raw tar.gz body (Content-Type: application/gzip),
// re-validates it server-side (defense in depth on top of client-side
// `apreg validate`), and stores it.
func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.UserFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	scope := r.PathValue("scope")
	name := r.PathValue("name")
	version := r.PathValue("version")

	tmpDir, err := os.MkdirTemp("", "apreg-publish-*")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer os.RemoveAll(tmpDir)

	tarballPath := filepath.Join(tmpDir, "upload.tar.gz")
	tarballFile, err := os.Create(tarballPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
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

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
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
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tarballFile.Close()

	v, err := s.reg.Publish(r.Context(), registry.PublishInput{
		Scope:      scope,
		Name:       name,
		Version:    version,
		Tarball:    tarballFile,
		Bundle:     bundle,
		RequestorU: user.Username,
	})
	if err != nil {
		writePublishError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"scope":        v.Scope,
		"name":         v.Name,
		"version":      v.Version,
		"checksum":     v.Checksum,
		"published_at": v.PublishedAt,
	})
}

func writePublishError(w http.ResponseWriter, err error) {
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
		writeError(w, http.StatusInternalServerError, "internal error")
	}
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
		writeStoreError(w, err)
		return
	}
	versions, err := s.reg.ListVersions(r.Context(), scope, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	versionStrings := make([]string, len(versions))
	for i, v := range versions {
		versionStrings[i] = v.Version
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scope":          p.Scope,
		"name":           p.Name,
		"description":    p.Description,
		"homepage":       p.Homepage,
		"repository":     p.Repository,
		"license":        p.License,
		"keywords":       p.Keywords,
		"latest_version": p.LatestVersion,
		"versions":       versionStrings,
		"created_at":     p.CreatedAt,
		"updated_at":     p.UpdatedAt,
	})
}

func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	scope, name, version := r.PathValue("scope"), r.PathValue("name"), r.PathValue("version")
	v, err := s.reg.ResolveVersion(r.Context(), scope, name, version)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope":        v.Scope,
		"name":         v.Name,
		"version":      v.Version,
		"checksum":     v.Checksum,
		"manifest":     rawJSON(v.ManifestJSON),
		"published_at": v.PublishedAt,
		"download_url": "/v1/plugins/" + scope + "/" + name + "/" + v.Version + "/download",
	})
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	scope, name, version := r.PathValue("scope"), r.PathValue("name"), r.PathValue("version")
	resolved, err := s.reg.ResolveVersion(r.Context(), scope, name, version)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	rc, err := s.reg.OpenTarball(r.Context(), scope, name, resolved.Version)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-Checksum-Sha256", resolved.Checksum)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+"-"+resolved.Version+`.tar.gz"`)
	io.Copy(w, rc)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	results, err := s.reg.Search(r.Context(), q)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
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

func writeStoreError(w http.ResponseWriter, err error) {
	if err == store.ErrNotFound {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}
