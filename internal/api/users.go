package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/pkulkarni/apreg/internal/auth"
	"github.com/pkulkarni/apreg/internal/store"
)

var errPasswordTooShort = errors.New("password must be at least 8 characters")

type signupRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if !auth.ValidUsername(req.Username) {
		writeError(w, http.StatusBadRequest, "username must be 1-40 lowercase alphanumeric/hyphen characters, not starting or ending with a hyphen")
		return
	}
	if req.Email == "" {
		writeError(w, http.StatusBadRequest, "email is required")
		return
	}
	if err := validatePassword(req.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	u, err := s.reg.Signup(r.Context(), req.Username, req.Email, hash)
	if err != nil {
		if err == store.ErrConflict {
			writeError(w, http.StatusConflict, "username or email already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.reg.RequestEmailVerification(r.Context(), u); err != nil {
		// Verification is advisory in v1 (see internal/registry doc
		// comment) — don't fail account creation over it, just log.
		slog.Warn("failed to send verification email", "username", u.Username, "error", err)
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"username":   u.Username,
		"email":      u.Email,
		"created_at": u.CreatedAt,
	})
}

type tokenRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Label    string `json:"label"`
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req tokenRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	if s.lockout.Locked(req.Username) {
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts for this account, try again later")
		return
	}

	u, err := s.reg.UserByUsername(r.Context(), req.Username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		s.lockout.RecordFailure(req.Username)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.lockout.Reset(req.Username)

	plaintext, err := auth.NewAPIToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	label := req.Label
	if label == "" {
		label = "cli"
	}
	if _, err := s.reg.IssueToken(r.Context(), u.ID, auth.HashToken(plaintext), label); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":    plaintext,
		"username": u.Username,
	})
}

type verifyEmailRequest struct {
	Token string `json:"token"`
}

func (s *Server) handleVerifyEmail(w http.ResponseWriter, r *http.Request) {
	var req verifyEmailRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if err := s.reg.ConfirmEmailVerification(r.Context(), req.Token); err != nil {
		if err == store.ErrTokenInvalid {
			writeError(w, http.StatusBadRequest, "token invalid, expired, or already used")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"verified": true})
}

type passwordResetRequest struct {
	Username string `json:"username"`
}

func (s *Server) handleRequestPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req passwordResetRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if err := s.reg.RequestPasswordReset(r.Context(), req.Username); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Always the same response, whether or not the username exists — see
	// registry.RequestPasswordReset's doc comment on why.
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "if that account exists, a reset link has been sent to its registered email",
	})
}

type passwordResetConfirmRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func (s *Server) handleConfirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var req passwordResetConfirmRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := s.reg.ConfirmPasswordReset(r.Context(), req.Token, hash); err != nil {
		if err == store.ErrTokenInvalid {
			writeError(w, http.StatusBadRequest, "token invalid, expired, or already used")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}

func validatePassword(password string) error {
	if len(password) < 8 {
		return errPasswordTooShort
	}
	return nil
}
