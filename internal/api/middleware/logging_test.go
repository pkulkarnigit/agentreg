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

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !strings.Contains(buf.String(), "status=200") {
		t.Errorf("expected default status 200 in log output, got: %s", buf.String())
	}
}
