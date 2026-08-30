// Package middleware holds cross-cutting HTTP concerns (auth today; the
// natural home for future rate limiting) kept out of handler bodies.
package middleware

import (
	"context"
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
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}
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
