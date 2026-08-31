package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstall_ExplicitVersionAndDirFlag installs an older, explicitly
// pinned version into a custom --dir, against a server that also has a
// newer version published — proving install doesn't just always grab
// latest, and that --dir is actually honored (both by the files on disk
// and by what gets recorded in krate-lock.json).
func TestInstall_ExplicitVersionAndDirFlag(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	if err := cmdInit([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}
	if err := cmdPublish([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdPublish v1: %v", err)
	}
	bumpToV2(t, "plugin-src")
	if err := cmdPublish([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdPublish v2: %v", err)
	}

	if err := cmdInstall([]string{"-dir", "custom-path", "@alice/plugin-src@0.1.0"}); err != nil {
		t.Fatalf("cmdInstall pinned v0.1.0: %v", err)
	}

	assertFileExists(t, filepath.Join("custom-path", "skills", "example", "SKILL.md"))
	assertFileMissing(t, filepath.Join("agent_plugins", "plugin-src"))

	lf, err := loadLockfile()
	if err != nil {
		t.Fatalf("loadLockfile: %v", err)
	}
	entry, ok := lf.Packages["@alice/plugin-src"]
	if !ok {
		t.Fatal("expected @alice/plugin-src tracked after install")
	}
	if entry.Version != "0.1.0" {
		t.Fatalf("expected pinned version 0.1.0 despite a newer 0.2.0 existing, got %s", entry.Version)
	}
	if entry.Dir != "custom-path" {
		t.Fatalf("expected locked dir custom-path (from --dir), got %s", entry.Dir)
	}
}

// TestInstall_NotFound confirms installing a plugin/version that doesn't
// exist fails clearly and leaves no trace — no half-created install
// directory, no lockfile entry.
func TestInstall_NotFound(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	err := cmdInstall([]string{"@alice/does-not-exist"})
	if err == nil {
		t.Fatal("expected an error installing a plugin that was never published")
	}

	assertFileMissing(t, filepath.Join("agent_plugins", "does-not-exist"))
	lf, err := loadLockfile()
	if err != nil {
		t.Fatalf("loadLockfile: %v", err)
	}
	if len(lf.Packages) != 0 {
		t.Fatalf("expected no lockfile entries after a failed install, got %v", lf.Packages)
	}
}

// TestInstall_ChecksumMismatch points install at a server that reports one
// checksum in metadata but serves content that hashes to something else —
// the situation the checksum verification exists to catch (a corrupted
// download, a compromised or misbehaving mirror). A real krate-server
// never produces this on its own, so this needs a hand-built fake one.
// Confirms install fails *and* never touches disk — the checksum check
// must happen strictly before any file is written, not after.
func TestInstall_ChecksumMismatch(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	content := []byte("this is not actually what the checksum below describes")
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/plugins/bob/evil/latest", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"version":  "1.0.0",
			"checksum": strings.Repeat("0", 64), // deliberately wrong
		})
	})
	mux.HandleFunc("/v1/plugins/bob/evil/1.0.0/download", func(w http.ResponseWriter, r *http.Request) {
		w.Write(content)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	if err := saveConfig(&cliConfig{Registry: srv.URL, Username: "bob"}); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	err := cmdInstall([]string{"@bob/evil"})
	if err == nil {
		t.Fatal("expected a checksum mismatch error")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected a checksum mismatch error, got: %v", err)
	}
	assertFileMissing(t, filepath.Join("agent_plugins", "evil"))

	// Sanity: prove the mismatch is real, not a test bug — the server's
	// claimed checksum genuinely doesn't match the content it served.
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) == strings.Repeat("0", 64) {
		t.Fatal("test bug: content accidentally hashes to the fake checksum")
	}
}

// TestPublish_NotLoggedIn confirms publish fails with a clear, specific
// error before ever touching the network or the filesystem when there's
// no stored credentials at all.
func TestPublish_NotLoggedIn(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	err := cmdPublish([]string{"whatever-does-not-even-need-to-exist"})
	if err == nil {
		t.Fatal("expected an error publishing while not logged in")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("expected a clear not-logged-in error, got: %v", err)
	}
}

// TestPublish_ValidationFailure confirms a plugin directory that fails
// local validation (invalid plugin.json here) never reaches the network —
// publish is supposed to catch this client-side before uploading anything.
func TestPublish_ValidationFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "plugin.json"), []byte(`{"not": "a valid plugin manifest"}`), 0o644); err != nil {
		t.Fatalf("write bad plugin.json: %v", err)
	}

	err := cmdPublish([]string{badDir})
	if err == nil {
		t.Fatal("expected a validation error publishing an invalid plugin.json")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("expected a validation failed error, got: %v", err)
	}
}

// TestPublish_DuplicateVersion confirms republishing the exact same
// version fails with the server's conflict, matching the "versions are
// permanent" guarantee documented for users.
func TestPublish_DuplicateVersion(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	if err := cmdInit([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}
	if err := cmdPublish([]string{"plugin-src"}); err != nil {
		t.Fatalf("first publish: %v", err)
	}

	err := cmdPublish([]string{"plugin-src"})
	if err == nil {
		t.Fatal("expected an error republishing the same version")
	}
	if !strings.Contains(err.Error(), "publish failed") {
		t.Fatalf("expected a publish failed error, got: %v", err)
	}
}
