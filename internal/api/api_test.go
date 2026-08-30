package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkulkarni/apreg/internal/pack"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/sqlite"
)

func newTestServer(t *testing.T) *httptest.Server {
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
	return httptest.NewServer(NewHandler(reg))
}

func buildSampleTarball(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	pluginJSON := `{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "hello",
		"version": "1.0.0",
		"description": "test plugin"
	}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(dir, "skills", "example")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# example\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "out.tar.gz")
	if _, err := pack.Dir(dir, archive); err != nil {
		t.Fatalf("pack.Dir: %v", err)
	}
	b, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFullFlow(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	client := srv.Client()

	// 1. Signup
	signupBody, _ := json.Marshal(map[string]string{
		"username": "alice", "email": "alice@example.com", "password": "hunter22222",
	})
	resp, err := client.Post(srv.URL+"/v1/users", "application/json", bytes.NewReader(signupBody))
	if err != nil {
		t.Fatalf("signup request: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("signup: expected 201, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Get token
	tokenBody, _ := json.Marshal(map[string]string{"username": "alice", "password": "hunter22222"})
	resp, err = client.Post(srv.URL+"/v1/tokens", "application/json", bytes.NewReader(tokenBody))
	if err != nil {
		t.Fatalf("token request: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("token: expected 201, got %d", resp.StatusCode)
	}
	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if tokenResp.Token == "" {
		t.Fatal("expected non-empty token")
	}

	// 3. Publish
	tarball := buildSampleTarball(t)
	req, err := http.NewRequest("PUT", srv.URL+"/v1/plugins/alice/hello/1.0.0", bytes.NewReader(tarball))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenResp.Token)
	req.Header.Set("Content-Type", "application/gzip")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("publish request: %v", err)
	}
	body, _ := readAll(resp)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish: expected 201, got %d: %s", resp.StatusCode, body)
	}

	// 3b. Publishing under someone else's scope should be forbidden.
	req2, _ := http.NewRequest("PUT", srv.URL+"/v1/plugins/bob/hello/1.0.0", bytes.NewReader(tarball))
	req2.Header.Set("Authorization", "Bearer "+tokenResp.Token)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 publishing under another scope, got %d", resp2.StatusCode)
	}
	resp2.Body.Close()

	// 3c. No token at all should be rejected.
	req3, _ := http.NewRequest("PUT", srv.URL+"/v1/plugins/alice/hello/1.0.1", bytes.NewReader(tarball))
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 with no token, got %d", resp3.StatusCode)
	}
	resp3.Body.Close()

	// 4. Get plugin metadata
	resp, err = client.Get(srv.URL + "/v1/plugins/alice/hello")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get plugin: expected 200, got %d", resp.StatusCode)
	}
	var pluginResp struct {
		LatestVersion string   `json:"latest_version"`
		Versions      []string `json:"versions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pluginResp); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if pluginResp.LatestVersion != "1.0.0" || len(pluginResp.Versions) != 1 {
		t.Fatalf("unexpected plugin response: %+v", pluginResp)
	}

	// 5. Download and verify bytes round-trip
	resp, err = client.Get(srv.URL + "/v1/plugins/alice/hello/latest/download")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: expected 200, got %d", resp.StatusCode)
	}
	downloaded, _ := readAll(resp)
	if !bytes.Equal(downloaded, tarball) {
		t.Fatal("downloaded bytes do not match uploaded tarball")
	}

	// 6. Search
	resp, err = client.Get(srv.URL + "/v1/search?q=hello")
	if err != nil {
		t.Fatal(err)
	}
	var searchResp struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(searchResp.Results) != 1 {
		t.Fatalf("expected 1 search result, got %d", len(searchResp.Results))
	}
}

func readAll(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, err := buf.ReadFrom(resp.Body)
	return buf.Bytes(), err
}
