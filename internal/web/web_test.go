package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkulkarni/apreg/internal/manifest"
	"github.com/pkulkarni/apreg/internal/pack"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/sqlite"
)

func newTestServer(t *testing.T) (*httptest.Server, *registry.Registry) {
	t.Helper()
	meta, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { meta.Close() })
	blob, err := fsblob.Open(t.TempDir())
	if err != nil {
		t.Fatalf("fsblob.Open: %v", err)
	}
	reg := registry.New(meta, blob)
	return httptest.NewServer(NewHandler(reg, "")), reg
}

func publishSample(t *testing.T, reg *registry.Registry) {
	t.Helper()
	dir := t.TempDir()
	pluginJSON := `{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "hello",
		"version": "1.0.0",
		"description": "A friendly test plugin",
		"keywords": ["test", "demo"]
	}`
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pluginJSON), 0o644)
	skillDir := filepath.Join(dir, "skills", "greeter")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Greeter\n\nSays hello.\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(`{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
		"mcpServers": {"greeter-server": {"type": "stdio", "command": "greeter"}}
	}`), 0o644)

	archive := filepath.Join(t.TempDir(), "out.tar.gz")
	if _, err := pack.Dir(dir, archive); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	bundle, err := manifest.ValidateDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := reg.Signup(context.Background(), "alice", "alice@example.com", "hash"); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.Publish(context.Background(), registry.PublishInput{
		Scope: "alice", Name: "hello", Version: "1.0.0",
		Tarball: f, Bundle: bundle, RequestorU: "alice",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

func TestIndexPage(t *testing.T) {
	srv, reg := newTestServer(t)
	defer srv.Close()
	publishSample(t, reg)

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "@alice/hello") {
		t.Fatalf("expected index page to list @alice/hello, got:\n%s", body)
	}
}

func TestPluginPage(t *testing.T) {
	srv, reg := newTestServer(t)
	defer srv.Close()
	publishSample(t, reg)

	resp, err := http.Get(srv.URL + "/@alice/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		"@alice/hello",
		"apreg install @alice/hello@1.0.0",
		"Greeter",
		"Says hello",
		"greeter-server",
		"stdio",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected plugin page to contain %q, got:\n%s", want, body)
		}
	}
}

func TestPluginPage_ShowsDownloadCounts(t *testing.T) {
	srv, reg := newTestServer(t)
	defer srv.Close()
	publishSample(t, reg)

	for i := 0; i < 4; i++ {
		if err := reg.IncrementDownloadCount(context.Background(), "alice", "hello", "1.0.0"); err != nil {
			t.Fatalf("IncrementDownloadCount: %v", err)
		}
	}

	resp, err := http.Get(srv.URL + "/@alice/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readBody(t, resp)
	if !strings.Contains(body, "4 downloads") {
		t.Fatalf("expected page to show '4 downloads', got:\n%s", body)
	}
}

func TestPluginPage_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/@nobody/nothing")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestCatalogPage_NotConfigured(t *testing.T) {
	meta, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer meta.Close()
	blob, err := fsblob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(registry.New(meta, blob), ""))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when no catalog is configured, got %d", resp.StatusCode)
	}
}

func TestCatalogPage_ShowsResults(t *testing.T) {
	meta, err := sqlite.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer meta.Close()
	blob, err := fsblob.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	catalogPath := filepath.Join(t.TempDir(), "catalog.json")
	os.WriteFile(catalogPath, []byte(`{
		"generated_at": "2026-08-30T10:00:00Z",
		"source": "https://example.com/list",
		"results": [
			{"Name": "good-plugin", "Category": "Dev", "RepoURL": "https://github.com/a/good", "Author": "a", "AuthorURL": "https://github.com/a", "Description": "does good things", "Tag": "production", "Valid": true, "PluginName": "good-plugin", "PluginVersion": "1.0.0", "Skills": ["one"], "HasMCP": true},
			{"Name": "bad-plugin", "Category": "Dev", "RepoURL": "https://github.com/b/bad", "Author": "b", "AuthorURL": "https://github.com/b", "Description": "missing bits", "Tag": "production", "Valid": false, "ValidationError": "plugin has neither a skills/ directory nor an mcp.json"}
		]
	}`), 0o644)

	srv := httptest.NewServer(NewHandler(registry.New(meta, blob), catalogPath))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		"good-plugin", "does good things", "v1.0.0", "has MCP server",
		"bad-plugin", "missing bits", "plugin has neither a skills/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected catalog page to contain %q, got:\n%s", want, body)
		}
	}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
