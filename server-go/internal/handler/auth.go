package handler

import (
	"context"
	"encoding/json"
	"log"
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
	orgStore   storage.OrgStore // optional — resolves pending invites on user creation
}

func NewAuthHandler(userStore storage.UserStore, tokenStore storage.TokenStore) *AuthHandler {
	return &AuthHandler{userStore: userStore, tokenStore: tokenStore}
}

func (h *AuthHandler) SetOrgStore(orgStore storage.OrgStore) {
	h.orgStore = orgStore
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		httpError(w, "username, email, and password are required", http.StatusBadRequest)
		return
	}
	if !isValidEmail(req.Email) {
		httpError(w, "invalid email format", http.StatusBadRequest)
		return
	}
	if msg := isValidPassword(req.Password); msg != "" {
		httpError(w, msg, http.StatusBadRequest)
		return
	}

	// Check duplicates (indexed lookups)
	existing, _ := h.userStore.FindUserByUsername(r.Context(), req.Username)
	if existing != nil {
		httpError(w, "username already taken", http.StatusConflict)
		return
	}
	existingEmail, _ := h.userStore.FindUserByEmail(r.Context(), req.Email)
	if existingEmail != nil {
		httpError(w, "email already registered", http.StatusConflict)
		return
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		httpError(w, "internal error", http.StatusInternalServerError)
		return
	}

	// First-run auto-promotion: the very first registration on a fresh
	// platform becomes platform_admin so the setup wizard can complete
	// without a separate "promote" step. All subsequent registrations
	// keep the default "user" role and must be elevated by an admin.
	role := "user"
	if existing, _ := h.userStore.FindAllUsers(r.Context()); len(existing) == 0 {
		role = "platform_admin"
	}

	now := time.Now()
	user := &domain.User{
		ID:           auth.GenerateID(),
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: hash,
		Role:         role,
		Active:       true,
		CreatedAt:    &now,
	}

	if err := h.userStore.CreateUser(r.Context(), user); err != nil {
		httpError(w, "failed to create account", http.StatusInternalServerError)
		return
	}

	// First-run org bootstrap. The legacy startup path used to create a
	// default org when self-hosted mode found a single user; that path
	// runs at server boot, before the wizard's first registration. So
	// inline it here: when the auto-promoted platform_admin lands, also
	// create the default org and add them as owner. Idempotent (skips if
	// any org already exists). Best-effort — log but don't fail the
	// registration if the org creation fails.
	if role == "platform_admin" && h.orgStore != nil {
		if err := auth.BootstrapDefaultOrg(r.Context(), h.orgStore, user.ID); err != nil {
			log.Printf("WARN: bootstrap default org failed: %v", err)
		}
	}

	h.resolvePendingInvites(r.Context(), user)

	// Auto-login: create PAT
	raw := auth.GenerateToken()
	token := &domain.AccessToken{
		TokenHash:   auth.HashToken(raw),
		TokenPrefix: auth.TokenPrefix(raw),
		UserID:      user.ID,
		Name:        "registration",
		CreatedAt:   &now,
	}
	h.tokenStore.CreateToken(r.Context(), token)

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"token": raw,
		"user":  user,
	})
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

	h.resolvePendingInvites(r.Context(), user)

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, user)
}

func (h *AuthHandler) resolvePendingInvites(ctx context.Context, user *domain.User) {
	if h.orgStore == nil {
		return
	}
	invites, _ := h.orgStore.FindPendingInvitesByEmail(ctx, user.Email)
	for _, inv := range invites {
		h.orgStore.AddOrgMember(ctx, &domain.OrgMember{
			OrgID: inv.OrgID, UserID: user.ID, Role: inv.Role,
		})
		h.orgStore.DeletePendingInvite(ctx, inv.ID)
	}
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

// GetSetupStatus reports whether the platform has at least one admin user.
// Unauthenticated by design — the studio's setup wizard polls this to know
// when to render the create-admin step. Returns {hasAdmin: bool}.
func (h *AuthHandler) GetSetupStatus(w http.ResponseWriter, r *http.Request) {
	users, err := h.userStore.FindAllUsers(r.Context())
	if err != nil {
		httpError(w, "failed to read users", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"hasAdmin": len(users) > 0})
}
