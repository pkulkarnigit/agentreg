// Package web implements AgentReg's read-only, server-rendered browsing UI.
// Like internal/api, it calls only internal/registry — never a concrete
// store implementation.
package web

import (
	"embed"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkulkarni/apreg/internal/pack"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

// Each page is parsed together with layout.html into its own *template.Template
// rather than one shared set: since every page's "title"/"content" blocks
// share those same names (by design, so layout.html can invoke either),
// parsing all files into one namespace would let the last-parsed file's
// definitions silently win for every page.
var (
	indexTmpl  = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/index.html"))
	pluginTmpl = template.Must(template.ParseFS(templateFS, "templates/layout.html", "templates/plugin.html"))
)

type Server struct {
	reg *registry.Registry
}

func NewHandler(reg *registry.Registry) http.Handler {
	s := &Server{reg: reg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	// Go's ServeMux requires a wildcard to own its whole path segment, so
	// "@scope" can't be split into a literal "@" + "{scope}" wildcard in
	// the pattern; the leading "@" is stripped from {scopeAt} in the handler.
	mux.HandleFunc("GET /{scopeAt}/{name}", s.handlePlugin)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	plugins, err := s.reg.Search(r.Context(), q)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	render(w, indexTmpl, map[string]any{"Plugins": plugins, "Query": q})
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
