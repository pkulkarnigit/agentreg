// Package middleware holds cross-cutting HTTP concerns (auth today; the
// natural home for future rate limiting) kept out of handler bodies.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/pkulkarni/apreg/internal/auth"
	"github.com/pkulkarni/apreg/internal/registry"
	"github.com/pkulkarni/apreg/internal/store"
)

type ctxKey int

const userCtxKey ctxKey = iota

// RequireAuth extracts and validates the "Authorization: Bearer <token>"
// header, resolves it to a user via reg, and stores that user on the
// request context for downstream handlers. Responds 401 on any failure.
func RequireAuth(reg *registry.Registry, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) {
			http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
			return
		}
		token := strings.TrimPrefix(h, prefix)
		user, err := reg.Authenticate(r.Context(), auth.HashToken(token))
		if err != nil {
			if err == store.ErrNotFound {
				slog.Warn("rejected request with invalid bearer token", "path", r.URL.Path)
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}
			slog.Error("authenticate failed", "path", r.URL.Path, "error", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, user)
		next(w, r.WithContext(ctx))
	}
}

// UserFromContext returns the authenticated user stored by RequireAuth.
func UserFromContext(ctx context.Context) (*store.User, bool) {
	u, ok := ctx.Value(userCtxKey).(*store.User)
	return u, ok
}

// RequireAdmin gates next to usernames in admins, an allowlist configured
// by whoever operates this instance (see cmd/apreg-server's
// KRATE_ADMIN_USERNAMES) — there's no admin flag on the user record
// itself, no promotion flow, nothing stored. Must run downstream of
// RequireAuth, which is what actually puts a user on the context; a
// missing user here means RequireAdmin was wired up without it, not that
// the request lacks a token, so that's a 500, not a 401. Responds 403 for
// a real, authenticated user who's simply not on the list.
func RequireAdmin(admins map[string]bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFromContext(r.Context())
		if !ok {
			slog.Error("RequireAdmin wired up without RequireAuth upstream", "path", r.URL.Path)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		if !admins[user.Username] {
			slog.Warn("rejected non-admin request to admin endpoint", "username", user.Username, "path", r.URL.Path)
			http.Error(w, `{"error":"forbidden: admin access required"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
