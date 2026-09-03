package middleware

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogging_CapturesMethodPathStatus(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := Logging(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/users", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	out := buf.String()
	for _, want := range []string{"POST", "/v1/users", "status=201", "127.0.0.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected log output to contain %q, got: %s", want, out)
		}
	}
}

func TestLogging_DefaultsStatusTo200IfUnset(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	handler := Logging(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok")) // no explicit WriteHeader
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/search", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !strings.Contains(buf.String(), "status=200") {
		t.Errorf("expected default status 200 in log output, got: %s", buf.String())
	}
}

// TestLogging_LevelScalesWithStatus is the actual point of this file:
// a 5xx needs to stand out as an ERROR (that's what an operator watching
// only errors needs to see), a 4xx is a WARN, and everything else is INFO
// — an ordinary "at info level" run must show a 500's request log, not
// bury it at the same level as routine traffic.
func TestLogging_LevelScalesWithStatus(t *testing.T) {
	newLogger := func(buf *bytes.Buffer) *slog.Logger {
		return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}

	cases := []struct {
		name       string
		status     int
		wantLevel  string
		wantLogged bool
	}{
		{"5xx is ERROR", 500, "level=ERROR", true},
		{"4xx is WARN", 404, "level=WARN", true},
		{"2xx is INFO", 200, "level=INFO", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			handler := Logging(newLogger(&buf), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
			}))
			req := httptest.NewRequest(http.MethodGet, "/v1/search", nil)
			handler.ServeHTTP(httptest.NewRecorder(), req)

			out := buf.String()
			if c.wantLogged && !strings.Contains(out, c.wantLevel) {
				t.Errorf("status %d: expected %s in log output, got: %s", c.status, c.wantLevel, out)
			}
		})
	}
}

// TestLogging_HealthzIsDebugNotInfo confirms /healthz requests don't
// drown out real traffic at the default level: at INFO (the default),
// they're invisible; only asking for DEBUG shows them.
func TestLogging_HealthzIsDebugNotInfo(t *testing.T) {
	handler := func(buf *bytes.Buffer, level slog.Level) http.Handler {
		logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level}))
		return Logging(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	}

	var infoBuf bytes.Buffer
	handler(&infoBuf, slog.LevelInfo).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if infoBuf.Len() != 0 {
		t.Errorf("expected /healthz to produce no output at INFO level, got: %s", infoBuf.String())
	}

	var debugBuf bytes.Buffer
	handler(&debugBuf, slog.LevelDebug).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(debugBuf.String(), "/healthz") {
		t.Errorf("expected /healthz to appear once DEBUG is enabled, got: %s", debugBuf.String())
	}
}
