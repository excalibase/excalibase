package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

type AuthHandler struct {
	userStore  storage.UserStore
	tokenStore storage.TokenStore
}

func NewAuthHandler(userStore storage.UserStore, tokenStore storage.TokenStore) *AuthHandler {
	return &AuthHandler{userStore: userStore, tokenStore: tokenStore}
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}

	user, _ := h.userStore.FindUserByUsername(r.Context(), req.Username)
	if user == nil || !auth.CheckPassword(req.Password, user.PasswordHash) {
		httpError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	// Create PAT
	raw := auth.GenerateToken()
	now := time.Now()
	token := &domain.AccessToken{
		TokenHash:   auth.HashToken(raw),
		TokenPrefix: auth.TokenPrefix(raw),
		UserID:      user.ID,
		Name:        "login",
		CreatedAt:   &now,
	}

	if err := h.tokenStore.CreateToken(r.Context(), token); err != nil {
		httpError(w, "token creation failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"token": raw, // returned once, client stores it
		"user":  user,
	})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	writeJSON(w, user)
}

func (h *AuthHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, _ := h.userStore.FindAllUsers(r.Context())
	writeJSON(w, users)
}

func (h *AuthHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpError(w, "failed to hash password", http.StatusInternalServerError)
		return
	}

	now := time.Now()
	user := &domain.User{
		ID:           auth.GenerateID(),
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: hash,
		Role:         req.Role,
		Active:       true,
		CreatedAt:    &now,
	}

	if err := h.userStore.CreateUser(r.Context(), user); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, user)
}

func (h *AuthHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userId")
	h.userStore.DeleteUser(r.Context(), userID)
	writeJSON(w, map[string]string{"status": "deleted"})
}

func (h *AuthHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	tokens, _ := h.tokenStore.ListTokensByUser(r.Context(), user.ID)
	writeJSON(w, tokens)
}

func (h *AuthHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	raw := auth.GenerateToken()
	now := time.Now()
	token := &domain.AccessToken{
		TokenHash:   auth.HashToken(raw),
		TokenPrefix: auth.TokenPrefix(raw),
		UserID:      user.ID,
		Name:        req.Name,
		CreatedAt:   &now,
	}

	if err := h.tokenStore.CreateToken(r.Context(), token); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"token":  raw, // returned once
		"prefix": token.TokenPrefix,
		"name":   req.Name,
	})
}

func (h *AuthHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	tokenHash := chi.URLParam(r, "tokenHash")
	h.tokenStore.DeleteToken(r.Context(), tokenHash)
	writeJSON(w, map[string]string{"status": "revoked"})
}
