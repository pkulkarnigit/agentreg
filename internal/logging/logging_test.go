package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"trace":   LevelTrace,
		"TRACE":   LevelTrace,
		" Trace ": LevelTrace,
		"debug":   slog.LevelDebug,
		"Debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"":        slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"bogus":   slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestNew_DefaultLevelHidesDebugAndTrace is the actual point of this
// package: without an explicit level, Debug and Trace must not appear in
// the output at all.
func TestNew_DefaultLevelHidesDebugAndTrace(t *testing.T) {
	var buf bytes.Buffer
	logger := New("", &buf)

	logger.Info("visible info")
	logger.Debug("hidden debug")
	logger.Log(context.Background(), LevelTrace, "hidden trace")

	out := buf.String()
	if !strings.Contains(out, "visible info") {
		t.Errorf("expected info to appear, got:\n%s", out)
	}
	if strings.Contains(out, "hidden debug") {
		t.Errorf("expected debug to be hidden at the default level, got:\n%s", out)
	}
	if strings.Contains(out, "hidden trace") {
		t.Errorf("expected trace to be hidden at the default level, got:\n%s", out)
	}
}

func TestNew_DebugLevelShowsDebugButNotTrace(t *testing.T) {
	var buf bytes.Buffer
	logger := New("debug", &buf)

	logger.Debug("visible debug")
	logger.Log(context.Background(), LevelTrace, "hidden trace")

	out := buf.String()
	if !strings.Contains(out, "visible debug") {
		t.Errorf("expected debug to appear at debug level, got:\n%s", out)
	}
	if strings.Contains(out, "hidden trace") {
		t.Errorf("expected trace to still be hidden at debug level, got:\n%s", out)
	}
}

func TestNew_TraceLevelShowsEverythingAndLabelsCorrectly(t *testing.T) {
	var buf bytes.Buffer
	logger := New("trace", &buf)

	logger.Log(context.Background(), LevelTrace, "a trace line")

	out := buf.String()
	if !strings.Contains(out, "a trace line") {
		t.Errorf("expected trace line to appear at trace level, got:\n%s", out)
	}
	if !strings.Contains(out, "level=TRACE") {
		t.Errorf("expected the custom TRACE label (not a raw level number), got:\n%s", out)
	}
}

func TestNew_LevelsAreOrdered(t *testing.T) {
	// The whole scheme depends on this ordering holding.
	if !(LevelTrace < slog.LevelDebug && slog.LevelDebug < slog.LevelInfo &&
		slog.LevelInfo < slog.LevelWarn && slog.LevelWarn < slog.LevelError) {
		t.Fatalf("levels are not strictly ordered TRACE < DEBUG < INFO < WARN < ERROR: trace=%d debug=%d info=%d warn=%d error=%d",
			LevelTrace, slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError)
	}
}

func TestTrace_RespectsDefaultLogger(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	defer slog.SetDefault(old)

	slog.SetDefault(New("info", &buf))
	Trace("should not appear")
	if strings.Contains(buf.String(), "should not appear") {
		t.Errorf("expected Trace() to respect the default logger's level, got:\n%s", buf.String())
	}

	buf.Reset()
	slog.SetDefault(New("trace", &buf))
	Trace("should appear", "key", "value")
	if !strings.Contains(buf.String(), "should appear") {
		t.Errorf("expected Trace() to log at trace level, got:\n%s", buf.String())
	}
}
