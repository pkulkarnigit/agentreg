package main

import (
	"bufio"
	"context"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkulkarni/apreg/internal/notify"
)

// fakeSender captures the last account-recovery message sent, so tests can
// pull the real token out of it — the same way a user would read it out of
// their inbox. Safe for concurrent use: cmdResetPassword's test drives it
// from one goroutine while polling it from another.
type fakeSender struct {
	mu   sync.Mutex
	body string
}

func (f *fakeSender) Send(_ context.Context, to, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body = body
	return nil
}

func (f *fakeSender) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.body
}

// clear resets the captured body — needed before waiting for a *new*
// message when an earlier one (e.g. the signup verification email) may
// already have set it, or waitForToken would immediately return that
// stale message instead of actually waiting.
func (f *fakeSender) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.body = ""
}

var tokenInBodyRE = regexp.MustCompile(`(?:verify-email|--token) (\S+)`)

// withStdin temporarily points the CLI's shared stdin reader at script
// (each line is one prompt's answer), restoring the real one afterward.
// Reassigning stdinReader directly — rather than swapping os.Stdin — works
// because it's the same *bufio.Reader every promptLine/promptPassword call
// in this package already reads from.
func withStdin(t *testing.T, script string, fn func()) {
	t.Helper()
	old := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(script))
	defer func() { stdinReader = old }()
	fn()
}

func TestCmdSignup_Success(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()

	var err error
	withStdin(t, "alice\nalice@example.com\nhunter22222\n", func() {
		err = cmdSignup([]string{"--registry", srv.URL})
	})
	if err != nil {
		t.Fatalf("cmdSignup: %v", err)
	}

	// Prove the account actually works, not just that cmdSignup returned
	// nil: log in with the same credentials for real.
	withStdin(t, "alice\nhunter22222\n", func() {
		err = cmdLogin([]string{"--registry", srv.URL})
	})
	if err != nil {
		t.Fatalf("login with the account cmdSignup just created: %v", err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Token == "" || cfg.Username != "alice" {
		t.Fatalf("expected a saved token for alice, got %+v", cfg)
	}
}

func TestCmdSignup_UsernameConflict(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()

	withStdin(t, "alice\nalice@example.com\nhunter22222\n", func() {
		if err := cmdSignup([]string{"--registry", srv.URL}); err != nil {
			t.Fatalf("first signup: %v", err)
		}
	})

	var err error
	withStdin(t, "alice\nother@example.com\nhunter22222\n", func() {
		err = cmdSignup([]string{"--registry", srv.URL})
	})
	if err == nil {
		t.Fatal("expected an error signing up with a username that's already taken")
	}
}

func TestCmdLogin_WrongPassword(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()

	withStdin(t, "alice\nalice@example.com\nhunter22222\n", func() {
		if err := cmdSignup([]string{"--registry", srv.URL}); err != nil {
			t.Fatalf("signup: %v", err)
		}
	})

	var err error
	withStdin(t, "alice\nwrong-password\n", func() {
		err = cmdLogin([]string{"--registry", srv.URL})
	})
	if err == nil {
		t.Fatal("expected an error logging in with the wrong password")
	}

	// A failed login must not clobber ~/.apreg/config.json with a bad or
	// empty token — saveConfig is only reached on the success path, so no
	// config file should exist at all yet.
	if _, err := loadConfig(); err != nil {
		t.Fatalf("loadConfig after failed login: %v", err)
	}
	cfg, _ := loadConfig()
	if cfg.Token != "" {
		t.Fatalf("expected no token saved after a failed login, got %+v", cfg)
	}
}

func TestCmdVerifyEmail_Success(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, reg := newTestServer(t)
	defer srv.Close()
	sender := &fakeSender{}
	reg.SetSender(sender)

	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	m := tokenInBodyRE.FindStringSubmatch(sender.lastBody())
	if m == nil {
		t.Fatalf("could not find a verification token in the sent message: %q", sender.lastBody())
	}

	if err := cmdVerifyEmail([]string{m[1]}); err != nil {
		t.Fatalf("cmdVerifyEmail: %v", err)
	}
}

func TestCmdVerifyEmail_InvalidToken(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, _ := newTestServer(t)
	defer srv.Close()
	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	if err := cmdVerifyEmail([]string{"not-a-real-token"}); err == nil {
		t.Fatal("expected an error verifying with a forged token")
	}
}

// TestCmdResetPassword_Success drives the real interactive
// cmdResetPassword end to end — username prompt, server-generated token,
// new password prompt, confirm — the same way a person actually
// experiences it. The token doesn't exist until cmdResetPassword's own
// request-step generates and "emails" it partway through the call, so
// there's no way to script the whole answer sequence up front: this runs
// cmdResetPassword in its own goroutine, waits for the token to actually
// arrive at fakeSender, then feeds it (and the new password) through a
// pipe standing in for stdin.
func TestCmdResetPassword_Success(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, reg := newTestServer(t)
	defer srv.Close()
	sender := &fakeSender{}
	reg.SetSender(sender)

	signupAndLogin(t, srv.URL, "alice", "alice@example.com")
	// cmdResetPassword falls back to the registry from config when
	// --registry isn't given — matches how a real user, already logged
	// in on this machine, would run it.

	// signupAndLogin's own signup already sent a verification email
	// through this same sender — clear it so waitForToken below can't
	// mistake that leftover message for the reset token.
	sender.clear()

	pr, pw := io.Pipe()
	old := stdinReader
	stdinReader = bufio.NewReader(pr)
	defer func() { stdinReader = old }()

	done := make(chan error, 1)
	go func() { done <- cmdResetPassword(nil) }()

	writeLine(t, pw, "alice") // Username:

	token := waitForToken(t, sender)
	writeLine(t, pw, token)         // Reset token:
	writeLine(t, pw, "newhunter22") // New password:

	if err := <-done; err != nil {
		t.Fatalf("cmdResetPassword: %v", err)
	}

	// Prove the password actually changed: log in with it for real.
	var loginErr error
	withStdin(t, "alice\nnewhunter22\n", func() {
		loginErr = cmdLogin([]string{"--registry", srv.URL})
	})
	if loginErr != nil {
		t.Fatalf("login with the new password: %v", loginErr)
	}
}

func TestCmdResetPassword_TokenFlagSkipsRequestStep(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())

	srv, reg := newTestServer(t)
	defer srv.Close()
	sender := &fakeSender{}
	reg.SetSender(sender)

	signupAndLogin(t, srv.URL, "alice", "alice@example.com")

	// This is really pinning the fix for the bug the password-reset email
	// used to describe (see internal/registry.RequestPasswordReset's doc
	// comment): --token now genuinely exists as a flag. Before the fix,
	// this exact invocation didn't return an error at all — flag.ExitOnError
	// killed the whole process with "flag provided but not defined: -token"
	// before cmdResetPassword's body ever ran, so a passing test here (a
	// normal returned error, not a process exit) is itself the proof the
	// flag is recognized. It must then skip straight to the confirm step —
	// a single "New password:" prompt, nothing else.
	var err error
	withStdin(t, "some-new-password\n", func() {
		err = cmdResetPassword([]string{"--registry", srv.URL, "--token", "forged-token"})
	})
	if err == nil {
		t.Fatal("expected an error confirming with a forged token")
	}
}

// writeLine feeds one line to a stdin-standing-in io.Pipe, for tests that
// need to answer a running command's prompts one at a time rather than
// scripting the whole answer sequence up front (see
// TestCmdResetPassword_Success for why).
func writeLine(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := w.Write([]byte(s + "\n")); err != nil {
		t.Fatalf("write %q to stdin pipe: %v", s, err)
	}
}

// waitForToken polls sender for a token, since cmdResetPassword's request
// step happens concurrently with the test goroutine reading it.
func waitForToken(t *testing.T, sender *fakeSender) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if body := sender.lastBody(); body != "" {
			if m := tokenInBodyRE.FindStringSubmatch(body); m != nil {
				return m[1]
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the reset token to be sent")
	return ""
}

var _ notify.Sender = (*fakeSender)(nil)
