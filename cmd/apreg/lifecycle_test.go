package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkulkarni/apreg/internal/api"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store/fsblob"
	"github.com/pkulkarni/apreg/internal/store/sqlite"
)

// TestInstallListUninstall drives the real CLI command functions — init,
// publish, install, list, uninstall — against a real registry server, the
// same way a user actually would. It's the first end-to-end test on the
// CLI side of this codebase (previously only pure functions like parseRef
// were unit tested), covering the local package-tracking this turn adds:
// apreg-lock.json, `apreg list`, and `apreg uninstall`.
//
// It also pins the fix for a real bug found while building this: pack.Unpack
// only ever writes files present in the tarball being unpacked, so
// installing a new version directly on top of an old install left behind
// any file the new version dropped. Publishing a v2 that renames a skill
// and reinstalling proves the old skill's file is actually gone afterward,
// not just that the new one appeared.
func TestInstallListUninstall(t *testing.T) {
	workDir := t.TempDir()
	t.Chdir(workDir)
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()

	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	// Before anything's installed, list must say so — not just be silent
	// or error, since an empty apreg-lock.json (or none at all) is the
	// normal starting state for any directory.
	out, err := captureStdout(t, func() error { return cmdList(nil) })
	if err != nil {
		t.Fatalf("cmdList (empty): %v", err)
	}
	if !strings.Contains(out, "No plugins installed") {
		t.Fatalf("expected empty-state message, got %q", out)
	}

	// v1: init + publish.
	if err := cmdInit([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}
	if err := cmdPublish([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdPublish v1: %v", err)
	}

	// Install it and confirm the lockfile picked it up.
	if err := cmdInstall([]string{"@alice/plugin-src"}); err != nil {
		t.Fatalf("cmdInstall v1: %v", err)
	}
	installDir := filepath.Join("agent_plugins", "plugin-src")
	assertFileExists(t, filepath.Join(installDir, "skills", "example", "SKILL.md"))

	lf, err := loadLockfile()
	if err != nil {
		t.Fatalf("loadLockfile: %v", err)
	}
	entry, ok := lf.Packages["@alice/plugin-src"]
	if !ok {
		t.Fatal("expected @alice/plugin-src in apreg-lock.json after install")
	}
	if entry.Version != "0.1.0" {
		t.Fatalf("expected locked version 0.1.0, got %s", entry.Version)
	}
	if entry.Dir != installDir {
		t.Fatalf("expected locked dir %s, got %s", installDir, entry.Dir)
	}

	// cmdList itself — not just the lockfile data it reads — must report
	// what's actually installed.
	out, err = captureStdout(t, func() error { return cmdList(nil) })
	if err != nil {
		t.Fatalf("cmdList (after install): %v", err)
	}
	if !strings.Contains(out, "@alice/plugin-src@0.1.0") || !strings.Contains(out, installDir) {
		t.Fatalf("expected cmdList output to mention the installed plugin and its dir, got %q", out)
	}

	// v2: rename the example skill, bump the version, republish.
	bumpToV2(t, "plugin-src")
	if err := cmdPublish([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdPublish v2: %v", err)
	}

	// Reinstalling latest must both bring in the new skill AND remove the
	// old one — this is the actual regression test.
	if err := cmdInstall([]string{"@alice/plugin-src"}); err != nil {
		t.Fatalf("cmdInstall v2: %v", err)
	}
	assertFileExists(t, filepath.Join(installDir, "skills", "renamed", "SKILL.md"))
	assertFileMissing(t, filepath.Join(installDir, "skills", "example", "SKILL.md"))

	lf, err = loadLockfile()
	if err != nil {
		t.Fatalf("loadLockfile after v2 install: %v", err)
	}
	if lf.Packages["@alice/plugin-src"].Version != "0.2.0" {
		t.Fatalf("expected locked version 0.2.0 after reinstall, got %s", lf.Packages["@alice/plugin-src"].Version)
	}

	// Uninstall: files gone, lockfile entry gone.
	if err := cmdUninstall([]string{"@alice/plugin-src"}); err != nil {
		t.Fatalf("cmdUninstall: %v", err)
	}
	assertFileMissing(t, installDir)
	lf, err = loadLockfile()
	if err != nil {
		t.Fatalf("loadLockfile after uninstall: %v", err)
	}
	if _, ok := lf.Packages["@alice/plugin-src"]; ok {
		t.Fatal("expected @alice/plugin-src to be gone from apreg-lock.json after uninstall")
	}
	out, err = captureStdout(t, func() error { return cmdList(nil) })
	if err != nil {
		t.Fatalf("cmdList (after uninstall): %v", err)
	}
	if !strings.Contains(out, "No plugins installed") {
		t.Fatalf("expected empty-state message after uninstall, got %q", out)
	}

	// Uninstalling something never installed here should fail clearly,
	// not silently succeed or panic.
	if err := cmdUninstall([]string{"@alice/never-installed"}); err == nil {
		t.Fatal("expected an error uninstalling a plugin apreg-lock.json never tracked")
	}
}

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
	return httptest.NewServer(api.NewHandler(reg)), reg
}

// captureStdout runs fn with os.Stdout redirected to a pipe, returning
// whatever it printed. Needed because cmdList/cmdInstall/etc. all write
// straight to os.Stdout via fmt.Print*, so it's the only way to assert on
// their actual output rather than just the side effects (files, lockfile
// entries) they leave behind.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	fnErr := fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return buf.String(), fnErr
}

// signupAndLogin creates an account and stores a working ~/.apreg/config.json
// directly via the real HTTP endpoints, bypassing cmdSignup/cmdLogin's
// interactive stdin prompts — those aren't what this test is about.
func signupAndLogin(t *testing.T, registryURL, username, email string) {
	t.Helper()
	const password = "hunter22222"

	post := func(path string, body map[string]string) map[string]any {
		b, _ := json.Marshal(body)
		resp, err := http.Post(registryURL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("POST %s: HTTP %d", path, resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s response: %v", path, err)
		}
		return out
	}

	post("/v1/users", map[string]string{"username": username, "email": email, "password": password})
	tok := post("/v1/tokens", map[string]string{"username": username, "password": password})

	if err := saveConfig(&cliConfig{Registry: registryURL, Username: username, Token: tok["token"].(string)}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}
}

// bumpToV2 rewrites dir's plugin.json to version 0.2.0 and renames its
// scaffolded skill, simulating a real release that drops one skill and
// adds another.
func bumpToV2(t *testing.T, dir string) {
	t.Helper()
	pluginJSON := `{
		"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
		"name": "plugin-src",
		"version": "0.2.0",
		"description": "Describe what this plugin does.",
		"keywords": []
	}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pluginJSON), 0o644); err != nil {
		t.Fatalf("rewrite plugin.json: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "skills", "example")); err != nil {
		t.Fatalf("remove old skill: %v", err)
	}
	newSkillDir := filepath.Join(dir, "skills", "renamed")
	if err := os.MkdirAll(newSkillDir, 0o755); err != nil {
		t.Fatalf("mkdir new skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(newSkillDir, "SKILL.md"), []byte("---\nname: renamed\ndescription: v2 skill.\n---\n\n# renamed\n"), 0o644); err != nil {
		t.Fatalf("write new skill: %v", err)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be gone, stat err: %v", path, err)
	}
}
