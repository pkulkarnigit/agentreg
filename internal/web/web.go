// Package web implements AgentReg's read-only, server-rendered browsing UI.
// Like internal/api, it calls only internal/registry — never a concrete
// store implementation.
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pkulkarni/apreg/internal/crawler"
	"github.com/pkulkarni/apreg/internal/pack"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

var funcMap = template.FuncMap{
	"timeAgo": timeAgo,
}

// Each page is parsed together with layout.html into its own *template.Template
// rather than one shared set: since every page's "title"/"content" blocks
// share those same names (by design, so layout.html can invoke either),
// parsing all files into one namespace would let the last-parsed file's
// definitions silently win for every page.
var (
	indexTmpl   = template.Must(template.New("index").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/index.html"))
	pluginTmpl  = template.Must(template.New("plugin").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/plugin.html"))
	catalogTmpl = template.Must(template.New("catalog").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/catalog.html"))
	docsTmpl    = template.Must(template.New("docs").Funcs(funcMap).ParseFS(templateFS, "templates/layout.html", "templates/docs.html"))
)

type Server struct {
	reg         *registry.Registry
	catalogPath string
	mirrorScope string
}

// NewHandler builds the web UI. catalogPath, if non-empty, is where
// /catalog reads cmd/crawler's JSON output from (see internal/crawler) —
// leave empty to disable that page (it 404s with a clear message instead).
// mirrorScope is the account cmd/crawler -publish mirrors into, used only
// to link "mirrored copy" from each catalog entry to its live listing.
func NewHandler(reg *registry.Registry, catalogPath, mirrorScope string) http.Handler {
	s := &Server{reg: reg, catalogPath: catalogPath, mirrorScope: mirrorScope}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /catalog", s.handleCatalog)
	mux.HandleFunc("GET /docs", s.handleDocs)
	// Go's ServeMux requires a wildcard to own its whole path segment, so
	// "@scope" can't be split into a literal "@" + "{scope}" wildcard in
	// the pattern; the leading "@" is stripped from {scopeAt} in the handler.
	mux.HandleFunc("GET /{scopeAt}/{name}", s.handlePlugin)
	return mux
}

type indexStats struct {
	Plugins   int
	Scopes    int
	Downloads int64
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	plugins, err := s.reg.Search(r.Context(), q)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Stats only mean anything for the unfiltered (homepage) listing —
	// Search("") returns everything, so `plugins` already is that set.
	var stats indexStats
	if q == "" {
		scopes := make(map[string]struct{})
		for _, p := range plugins {
			scopes[p.Scope] = struct{}{}
			stats.Downloads += p.TotalDownloads
		}
		stats.Plugins = len(plugins)
		stats.Scopes = len(scopes)
	}

	render(w, indexTmpl, map[string]any{"Plugins": plugins, "Query": q, "Stats": stats})
}

// handleDocs serves a single static documentation page. It's plain
// content with no registry data behind it, so unlike the other pages
// there's no error path here beyond the template itself failing to render.
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	render(w, docsTmpl, nil)
}

// handleCatalog shows the crawler's most recent output (internal/crawler,
// run via cmd/crawler) — publicly known Agent Plugins discovered and
// validated, not anything published into this registry. Reads the JSON
// file fresh on every request rather than caching it in memory: it's a
// batch artifact refreshed by re-running the crawler, small (dozens of
// entries), and this way a redeployed catalog file shows up without a
// server restart.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	if s.catalogPath == "" {
		http.Error(w, "no catalog configured (APREG_CATALOG_PATH not set)", http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(s.catalogPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "no catalog has been generated yet — run cmd/crawler", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var cat crawler.Catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		http.Error(w, "catalog file is not valid JSON: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var valid, invalid []crawler.Result
	for _, res := range cat.Results {
		if res.Valid {
			valid = append(valid, res)
		} else {
			invalid = append(invalid, res)
		}
	}

	render(w, catalogTmpl, map[string]any{
		"Catalog":     cat,
		"Valid":       valid,
		"Invalid":     invalid,
		"MirrorScope": s.mirrorScope,
	})
}

type skillView struct {
	Name    string
	Preview string
}

type mcpServerView struct {
	Name   string
	Type   string
	Target string
}

func (s *Server) handlePlugin(w http.ResponseWriter, r *http.Request) {
	scopeAt, name := r.PathValue("scopeAt"), r.PathValue("name")
	scope, ok := strings.CutPrefix(scopeAt, "@")
	if !ok {
		http.NotFound(w, r)
		return
	}
	requestedVersion := r.URL.Query().Get("version")

	plugin, err := s.reg.GetPlugin(r.Context(), scope, name)
	if err != nil {
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	versions, err := s.reg.ListVersions(r.Context(), scope, name)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	version, err := s.reg.ResolveVersion(r.Context(), scope, name, requestedVersion)
	if err != nil {
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	skills, mcpServers := s.loadVersionContents(r, scope, name, version.Version)

	// Reverse-chronological doesn't matter much for a handful of versions;
	// keep table in the store's ascending-by-publish order.
	render(w, pluginTmpl, map[string]any{
		"Plugin":     plugin,
		"Versions":   versions,
		"Version":    version,
		"Skills":     skills,
		"MCPServers": mcpServers,
	})
}

// loadVersionContents opens the tarball for one version and extracts
// skill previews + MCP server listings for display. Best-effort: on any
// error it just omits that section rather than failing the page.
func (s *Server) loadVersionContents(r *http.Request, scope, name, version string) ([]skillView, []mcpServerView) {
	rc, err := s.reg.OpenTarball(r.Context(), scope, name, version)
	if err != nil {
		return nil, nil
	}
	defer rc.Close()

	tmpDir, err := os.MkdirTemp("", "apreg-web-*")
	if err != nil {
		return nil, nil
	}
	defer os.RemoveAll(tmpDir)

	tarballPath := filepath.Join(tmpDir, "bundle.tar.gz")
	f, err := os.Create(tarballPath)
	if err != nil {
		return nil, nil
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		return nil, nil
	}
	f.Close()

	extractDir := filepath.Join(tmpDir, "extracted")
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return nil, nil
	}
	if err := pack.Unpack(tarballPath, extractDir); err != nil {
		return nil, nil
	}

	var skills []skillView
	skillsDir := filepath.Join(extractDir, "skills")
	if entries, err := os.ReadDir(skillsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			content, err := os.ReadFile(filepath.Join(skillsDir, e.Name(), "SKILL.md"))
			if err != nil {
				continue
			}
			skills = append(skills, skillView{Name: e.Name(), Preview: truncate(string(content), 600)})
		}
	}

	var mcpServers []mcpServerView
	if raw, err := os.ReadFile(filepath.Join(extractDir, "mcp.json")); err == nil {
		var doc struct {
			MCPServers map[string]struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				URL     string `json:"url"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			for name, srv := range doc.MCPServers {
				target := srv.Command
				if target == "" {
					target = srv.URL
				}
				mcpServers = append(mcpServers, mcpServerView{Name: name, Type: srv.Type, Target: target})
			}
		}
	}

	return skills, mcpServers
}

// timeAgo renders a duration since t the way most package registries do
// ("3 days ago") rather than an absolute timestamp — easier to scan in a
// list of many entries.
func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return agoUnit(int(d/time.Minute), "minute")
	case d < 24*time.Hour:
		return agoUnit(int(d/time.Hour), "hour")
	case d < 30*24*time.Hour:
		return agoUnit(int(d/(24*time.Hour)), "day")
	case d < 365*24*time.Hour:
		return agoUnit(int(d/(30*24*time.Hour)), "month")
	default:
		return agoUnit(int(d/(365*24*time.Hour)), "year")
	}
}

func agoUnit(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "\n\n[…truncated…]"
}

func render(w http.ResponseWriter, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}
