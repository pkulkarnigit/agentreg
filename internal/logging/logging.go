// Package logging is the one place that knows how to build KrateAI's
// loggers, so every binary (krate-server, crawler) and every package that
// logs through them (they all just call the stdlib slog.Info/Warn/Error/
// Debug package-level functions against whatever New installed as the
// default logger) ends up with the same five levels, the same env var,
// and the same human-readable output — one small package instead of each
// binary reinventing it slightly differently.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// LevelTrace sits one tier below slog's own LevelDebug (-4), the same
// spacing slog uses between its four built-in levels. slog has no notion
// of TRACE itself — New's ReplaceAttr is what makes a logger actually
// print "TRACE" instead of the meaningless default rendering ("DEBUG-4")
// a slog handler would otherwise give a level it doesn't recognize.
const LevelTrace slog.Level = slog.LevelDebug - 4

// ParseLevel maps a level name (case-insensitive; "trace", "debug",
// "info", "warn"/"warning", "error") to its slog.Level. Empty or
// unrecognized input returns slog.LevelInfo — every place that reads
// KRATE_LOG_LEVEL treats "unset" and "info" identically, so a typo in the
// env var quietly falls back to the sensible default rather than
// producing an empty log stream or an error nobody sees.
func ParseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "trace":
		return LevelTrace
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New builds the logger every KrateAI binary should use: a text handler
// (grep-and-eyeball friendly, matching what this codebase has always
// logged in) over w, at the level ParseLevel(levelName) resolves to.
// DEBUG and TRACE are real levels here, not just log lines someone
// remembered to gate behind an if — they're simply below the default
// INFO threshold, so they exist in the code unconditionally and only
// actually print once an operator opts in via level.
func New(levelName string, w io.Writer) *slog.Logger {
	level := ParseLevel(levelName)
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceLevelAttr,
	}))
}

// replaceLevelAttr renders LevelTrace as "TRACE" — everything at or above
// slog.LevelDebug already has a name slog knows, so this only needs to
// special-case the one level slog doesn't.
func replaceLevelAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.LevelKey {
		if lvl, ok := a.Value.Any().(slog.Level); ok && lvl == LevelTrace {
			a.Value = slog.StringValue("TRACE")
		}
	}
	return a
}

// Trace logs at LevelTrace — the level below Debug, for detail that's
// noisy even during ordinary debugging (per-query traces, raw payload
// sizes) and only worth turning on when Debug itself isn't fine-grained
// enough. slog has Debug/Info/Warn/Error as package-level functions
// against the default logger; this fills the one gap, using
// slog.Default() the same way slog.Debug etc. do internally, and skipping
// the call entirely when nobody's listening at this level (the same
// short-circuit slog's own leveled functions already do, just one level
// lower than slog defines).
func Trace(msg string, args ...any) {
	TraceContext(context.Background(), msg, args...)
}

// TraceContext is Trace with an explicit context, matching slog's own
// DebugContext/InfoContext/etc. shape.
func TraceContext(ctx context.Context, msg string, args ...any) {
	logger := slog.Default()
	if !logger.Enabled(ctx, LevelTrace) {
		return
	}
	logger.Log(ctx, LevelTrace, msg, args...)
}
