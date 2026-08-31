package main

import (
	"strings"
	"testing"
)

func TestCmdSearch_FindsPublishedPlugin(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	if err := cmdInit([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}
	if err := cmdPublish([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdPublish: %v", err)
	}

	out, err := captureStdout(t, func() error { return cmdSearch([]string{"plugin-src"}) })
	if err != nil {
		t.Fatalf("cmdSearch: %v", err)
	}
	if !strings.Contains(out, "@alice/plugin-src@0.1.0") {
		t.Fatalf("expected search output to mention the published plugin, got %q", out)
	}
}

func TestCmdSearch_NoResults(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	out, err := captureStdout(t, func() error { return cmdSearch([]string{"nothing-published-matches-this"}) })
	if err != nil {
		t.Fatalf("cmdSearch: %v", err)
	}
	if !strings.Contains(out, "No results") {
		t.Fatalf("expected a no-results message, got %q", out)
	}
}

func TestCmdSearch_NoRegistryConfigured(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	if err := cmdSearch([]string{"anything"}); err == nil {
		t.Fatal("expected an error searching with no registry configured")
	}
}

func TestCmdView_Success(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	if err := cmdInit([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdInit: %v", err)
	}
	if err := cmdPublish([]string{"plugin-src"}); err != nil {
		t.Fatalf("cmdPublish: %v", err)
	}

	out, err := captureStdout(t, func() error { return cmdView([]string{"@alice/plugin-src"}) })
	if err != nil {
		t.Fatalf("cmdView: %v", err)
	}
	if !strings.Contains(out, `"version": "0.1.0"`) {
		t.Fatalf("expected view output to contain the resolved version, got %q", out)
	}
}

func TestCmdView_NotFound(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	if err := cmdView([]string{"@alice/never-published"}); err == nil {
		t.Fatal("expected an error viewing a plugin that was never published")
	}
}
