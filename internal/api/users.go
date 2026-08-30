package api

import (
	"encoding/json"
	"net/http"

	"github.com/pkulkarni/apreg/internal/auth"
	"github.com/pkulkarni/apreg/internal/store"
)

type signupRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !auth.ValidUsername(req.Username) {
		writeError(w, http.StatusBadRequest, "username must be 1-40 lowercase alphanumeric/hyphen characters, not starting or ending with a hyphen")
		return
	}
	if req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "email and password are required")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	u, err := s.reg.UserByUsername(r.Context(), req.Username)
	if err != nil || !auth.CheckPassword(u.PasswordHash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

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
