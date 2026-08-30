package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/pkulkarni/apreg/internal/auth"
	"github.com/pkulkarni/apreg/internal/pack"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/sqlite"
)

func newTestRegistry(t *testing.T) *registry.Registry {
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
	return registry.New(meta, blob)
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(NewHandler(newTestRegistry(t)))
}

// fakeSender captures notify.Sender calls so tests can pull a
// verification/reset token out of what would have been emailed.
type fakeSender struct{ body string }

func (f *fakeSender) Send(_ context.Context, to, subject, body string) error {
	f.body = body
	return nil
}

var tokenInBodyRE = regexp.MustCompile(`(?:verify-email|--token) (\S+)`)

func extractToken(t *testing.T, body string) string {
	t.Helper()
	m := tokenInBodyRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("could not find token in message body: %q", body)
	}
	return m[1]
}

func postJSON(t *testing.T, client *http.Client, url string, body map[string]string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := client.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
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

func TestDownloadCountIsTracked(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	client := srv.Client()

	postJSON(t, client, srv.URL+"/v1/users", map[string]string{
		"username": "alice", "email": "alice@example.com", "password": "hunter22222",
	}).Body.Close()
	tokResp := postJSON(t, client, srv.URL+"/v1/tokens", map[string]string{"username": "alice", "password": "hunter22222"})
	var tok struct {
		Token string `json:"token"`
	}
	json.NewDecoder(tokResp.Body).Decode(&tok)
	tokResp.Body.Close()

	tarball := buildSampleTarball(t)
	req, _ := http.NewRequest("PUT", srv.URL+"/v1/plugins/alice/hello/1.0.0", bytes.NewReader(tarball))
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readAll(resp)

	for i := 0; i < 3; i++ {
		dl, err := client.Get(srv.URL + "/v1/plugins/alice/hello/latest/download")
		if err != nil {
			t.Fatal(err)
		}
		readAll(dl)
	}

	var plugin struct {
		TotalDownloads int64 `json:"total_downloads"`
	}
	pResp, _ := client.Get(srv.URL + "/v1/plugins/alice/hello")
	json.NewDecoder(pResp.Body).Decode(&plugin)
	pResp.Body.Close()
	if plugin.TotalDownloads != 3 {
		t.Fatalf("expected total_downloads 3, got %d", plugin.TotalDownloads)
	}

	var version struct {
		DownloadCount int64 `json:"download_count"`
	}
	vResp, _ := client.Get(srv.URL + "/v1/plugins/alice/hello/1.0.0")
	json.NewDecoder(vResp.Body).Decode(&version)
	vResp.Body.Close()
	if version.DownloadCount != 3 {
		t.Fatalf("expected download_count 3, got %d", version.DownloadCount)
	}
}

func TestSignupRateLimit(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	client := srv.Client()

	for i := 0; i < SignupBurst; i++ {
		resp := postJSON(t, client, srv.URL+"/v1/users", map[string]string{
			"username": fmt.Sprintf("user%d", i), "email": fmt.Sprintf("user%d@example.com", i), "password": "hunter22222",
		})
		body, _ := readAll(resp)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("signup %d: expected 201, got %d: %s", i, resp.StatusCode, body)
		}
	}

	resp := postJSON(t, client, srv.URL+"/v1/users", map[string]string{
		"username": "onemore", "email": "onemore@example.com", "password": "hunter22222",
	})
	body, _ := readAll(resp)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding signup burst, got %d: %s", resp.StatusCode, body)
	}
}

func TestLoginRateLimit_PerIP(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()
	client := srv.Client()

	// Distinct usernames per attempt so per-username lockout (also
	// threshold 10) never engages — isolates the per-IP rate limiter.
	for i := 0; i < LoginBurst; i++ {
		resp := postJSON(t, client, srv.URL+"/v1/tokens", map[string]string{
			"username": fmt.Sprintf("nouser%d", i), "password": "wrong",
		})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("login attempt %d: expected 401, got %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp := postJSON(t, client, srv.URL+"/v1/tokens", map[string]string{"username": "yetanother", "password": "wrong"})
	body, _ := readAll(resp)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after exceeding login burst, got %d: %s", resp.StatusCode, body)
	}
}

func TestLoginLockout_PerUsername(t *testing.T) {
	// Exercises handleCreateToken directly (bypassing the outer
	// RateLimit middleware, which shares the same threshold and would
	// otherwise trip at the same request count, making it impossible to
	// tell which mechanism actually fired).
	reg := newTestRegistry(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct-password")
	if _, err := reg.Signup(ctx, "victim", "victim@example.com", hash); err != nil {
		t.Fatalf("Signup: %v", err)
	}

	s := &Server{reg: reg, lockout: auth.NewLoginLockout(3, time.Hour)}

	for i := 0; i < 3; i++ {
		body, _ := json.Marshal(map[string]string{"username": "victim", "password": "wrong"})
		req := httptest.NewRequest(http.MethodPost, "/v1/tokens", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.handleCreateToken(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401, got %d", i, rec.Code)
		}
	}

	// Locked out now, even with the CORRECT password.
	body, _ := json.Marshal(map[string]string{"username": "victim", "password": "correct-password"})
	req := httptest.NewRequest(http.MethodPost, "/v1/tokens", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleCreateToken(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 (locked out) even with correct password, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestEmailVerification_EndToEnd(t *testing.T) {
	reg := newTestRegistry(t)
	sender := &fakeSender{}
	reg.SetSender(sender)
	srv := httptest.NewServer(NewHandler(reg))
	defer srv.Close()
	client := srv.Client()

	resp := postJSON(t, client, srv.URL+"/v1/users", map[string]string{
		"username": "alice", "email": "alice@example.com", "password": "hunter22222",
	})
	resp.Body.Close()
	token := extractToken(t, sender.body)

	verifyResp := postJSON(t, client, srv.URL+"/v1/users/verify", map[string]string{"token": token})
	if verifyResp.StatusCode != http.StatusOK {
		body, _ := readAll(verifyResp)
		t.Fatalf("expected 200, got %d: %s", verifyResp.StatusCode, body)
	}
	verifyResp.Body.Close()

	// Reusing the token must fail.
	reuseResp := postJSON(t, client, srv.URL+"/v1/users/verify", map[string]string{"token": token})
	if reuseResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 reusing a consumed token, got %d", reuseResp.StatusCode)
	}
	reuseResp.Body.Close()
}

func TestPasswordReset_EndToEnd(t *testing.T) {
	reg := newTestRegistry(t)
	sender := &fakeSender{}
	reg.SetSender(sender)
	srv := httptest.NewServer(NewHandler(reg))
	defer srv.Close()
	client := srv.Client()

	postJSON(t, client, srv.URL+"/v1/users", map[string]string{
		"username": "bob", "email": "bob@example.com", "password": "original22",
	}).Body.Close()

	reqResp := postJSON(t, client, srv.URL+"/v1/password-reset/request", map[string]string{"username": "bob"})
	if reqResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", reqResp.StatusCode)
	}
	reqResp.Body.Close()
	token := extractToken(t, sender.body)

	confirmResp := postJSON(t, client, srv.URL+"/v1/password-reset/confirm", map[string]string{
		"token": token, "new_password": "brandnew22",
	})
	if confirmResp.StatusCode != http.StatusOK {
		body, _ := readAll(confirmResp)
		t.Fatalf("expected 200, got %d: %s", confirmResp.StatusCode, body)
	}
	confirmResp.Body.Close()

	// Old password no longer works.
	oldLogin := postJSON(t, client, srv.URL+"/v1/tokens", map[string]string{"username": "bob", "password": "original22"})
	if oldLogin.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old password to be rejected, got %d", oldLogin.StatusCode)
	}
	oldLogin.Body.Close()

	// New password works.
	newLogin := postJSON(t, client, srv.URL+"/v1/tokens", map[string]string{"username": "bob", "password": "brandnew22"})
	if newLogin.StatusCode != http.StatusCreated {
		body, _ := readAll(newLogin)
		t.Fatalf("expected new password to work, got %d: %s", newLogin.StatusCode, body)
	}
	newLogin.Body.Close()
}

func TestSignup_RejectsOversizedBody(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	// A body larger than maxJSONBodyBytes wrapped around a small amount
	// of valid-looking JSON structure, so this actually exercises the
	// size cap rather than just failing to parse for unrelated reasons.
	huge := make([]byte, maxJSONBodyBytes+1024)
	for i := range huge {
		huge[i] = 'a'
	}
	body := []byte(`{"username":"` + string(huge) + `","email":"a@example.com","password":"hunter22222"}`)

	resp, err := srv.Client().Post(srv.URL+"/v1/users", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		respBody, _ := readAll(resp)
		t.Fatalf("expected 400 for oversized body, got %d: %s", resp.StatusCode, respBody)
	}
}

func TestPasswordReset_UnknownUsernameStillReturns200(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	resp := postJSON(t, srv.Client(), srv.URL+"/v1/password-reset/request", map[string]string{"username": "nobody"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 even for an unknown username (must not leak account existence), got %d", resp.StatusCode)
	}
}
