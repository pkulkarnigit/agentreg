package crawler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func samplePluginDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "mirrored-plugin",
		"version": "1.0.0"
	}`), 0o644)
	skillDir := filepath.Join(dir, "skills", "example")
	os.MkdirAll(skillDir, 0o755)
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# example\n"), 0o644)
	return dir
}

func TestPublishToRegistry_Success(t *testing.T) {
	var gotAuth, gotPath, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	cfg := PublishConfig{RegistryURL: srv.URL, Scope: "github-mirror", Token: "sekret-token"}
	err := publishToRegistry(context.Background(), srv.Client(), cfg, samplePluginDir(t), "mirrored-plugin", "1.0.0", time.Time{})
	if err != nil {
		t.Fatalf("publishToRegistry: %v", err)
	}
	if gotAuth != "Bearer sekret-token" {
		t.Errorf("Authorization header = %q", gotAuth)
	}
	if gotPath != "/v1/plugins/github-mirror/mirrored-plugin/1.0.0" {
		t.Errorf("path = %q", gotPath)
	}
	if gotContentType != "application/gzip" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
}

func TestPublishToRegistry_SendsUpstreamPublishedAt(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("published_at")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	upstream := time.Date(2019, 3, 14, 9, 26, 53, 0, time.UTC)
	cfg := PublishConfig{RegistryURL: srv.URL, Scope: "github-mirror", Token: "t"}
	err := publishToRegistry(context.Background(), srv.Client(), cfg, samplePluginDir(t), "mirrored-plugin", "1.0.0", upstream)
	if err != nil {
		t.Fatalf("publishToRegistry: %v", err)
	}
	if gotQuery != "2019-03-14T09:26:53Z" {
		t.Errorf("published_at query param = %q, want 2019-03-14T09:26:53Z", gotQuery)
	}
}

func TestPublishToRegistry_ZeroPublishedAtOmitsQueryParam(t *testing.T) {
	var gotRawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRawQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	cfg := PublishConfig{RegistryURL: srv.URL, Scope: "github-mirror", Token: "t"}
	err := publishToRegistry(context.Background(), srv.Client(), cfg, samplePluginDir(t), "mirrored-plugin", "1.0.0", time.Time{})
	if err != nil {
		t.Fatalf("publishToRegistry: %v", err)
	}
	if gotRawQuery != "" {
		t.Errorf("expected no query string for a zero-value published_at, got %q", gotRawQuery)
	}
}

func TestPublishToRegistry_ConflictIsAlreadyPublished(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	cfg := PublishConfig{RegistryURL: srv.URL, Scope: "github-mirror", Token: "t"}
	err := publishToRegistry(context.Background(), srv.Client(), cfg, samplePluginDir(t), "x", "1.0.0", time.Time{})
	if !errors.Is(err, errAlreadyPublished) {
		t.Fatalf("expected errAlreadyPublished for HTTP 409, got %v", err)
	}
}

func TestPublishToRegistry_OtherFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"forbidden: token does not own this scope"}`))
	}))
	defer srv.Close()

	cfg := PublishConfig{RegistryURL: srv.URL, Scope: "someone-else", Token: "t"}
	err := publishToRegistry(context.Background(), srv.Client(), cfg, samplePluginDir(t), "x", "1.0.0", time.Time{})
	if err == nil || errors.Is(err, errAlreadyPublished) {
		t.Fatalf("expected a real error for HTTP 403, got %v", err)
	}
}

func TestVersionAlreadyPublished(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path == "/v1/plugins/github-mirror/exists/1.0.0" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cfg := PublishConfig{RegistryURL: srv.URL, Scope: "github-mirror", Token: "t"}

	got, err := versionAlreadyPublished(context.Background(), srv.Client(), cfg, "exists", "1.0.0")
	if err != nil {
		t.Fatalf("versionAlreadyPublished: %v", err)
	}
	if !got {
		t.Error("expected true for a version the registry already has")
	}

	got, err = versionAlreadyPublished(context.Background(), srv.Client(), cfg, "missing", "1.0.0")
	if err != nil {
		t.Fatalf("versionAlreadyPublished: %v", err)
	}
	if got {
		t.Error("expected false for a version the registry doesn't have")
	}
}
