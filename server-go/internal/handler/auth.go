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

const errNotAuthenticated = "not authenticated"

type AuthHandler struct {
	userStore     storage.UserStore
	tokenStore    storage.TokenStore
	orgStore      storage.OrgStore      // optional — joins an org through an invite token at registration
	instanceStore storage.InstanceStore // optional — lets CreateToken bind a PAT to a project the caller can see
	auditLog      auditWriter           // optional — records PAT rotations
	inviteOnly    bool                  // when true, only an invite token (or the first admin) may register
}

// SetInstanceStore wires the instance store CreateToken uses to confirm the
// caller may see the project a new PAT is bound to.
func (h *AuthHandler) SetInstanceStore(s storage.InstanceStore) { h.instanceStore = s }

func NewAuthHandler(userStore storage.UserStore, tokenStore storage.TokenStore) *AuthHandler {
	return &AuthHandler{userStore: userStore, tokenStore: tokenStore}
}

// SetAuditLog enables audit entries for token rotation. Nil (the default)
// keeps rotation working without an audit sink.
func (h *AuthHandler) SetAuditLog(auditLog auditWriter) { h.auditLog = auditLog }

// SetInviteOnly closes open self-registration: once the platform has its first
// admin, only a registration carrying a live org invite token may proceed. Default (false)
// keeps registration open for back-compat / self-hosted single-tenant use.
func (h *AuthHandler) SetInviteOnly(v bool) { h.inviteOnly = v }

func (h *AuthHandler) SetOrgStore(orgStore storage.OrgStore) {
	h.orgStore = orgStore
}

func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		InviteToken string `json:"inviteToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if status, msg := h.checkRegistration(r.Context(), req.Username, req.Email, req.Password); status != 0 {
		httpError(w, msg, status)
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

	inviteHash, status, msg := h.checkRegistrationInvite(r.Context(), req.InviteToken, role == "platform_admin")
	if status != 0 {
		httpError(w, msg, status)
		return
	}

	now := time.Now()
	user := &domain.User{
		ID:           auth.GenerateID(),
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: hash,
		Role:         role,
		Active:       true,
		Kind:         domain.UserKindHuman,
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

	if inviteHash != "" {
		if status, msg := h.joinInvitedOrg(r.Context(), inviteHash, user); status != 0 {
			httpError(w, msg, status)
			return
		}
	}

	// Auto-login: a bounded session token, same shape as Login issues.
	raw := auth.GenerateToken()
	expiry := now.Add(sessionTokenTTL)
	token := &domain.AccessToken{
		TokenHash:   auth.HashToken(raw),
		TokenPrefix: auth.TokenPrefix(raw),
		UserID:      user.ID,
		Name:        "registration",
		Scopes:      "session",
		CreatedAt:   &now,
		ExpiresAt:   &expiry,
	}
	if err := h.tokenStore.CreateToken(r.Context(), token); err != nil {
		// Don't 200 with a token the user can never use again — that would
		// trap them in a loop where every subsequent request 401s.
		httpError(w, "token persistence failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]interface{}{
		"token":     raw,
		"expiresAt": expiry,
		"user":      user,
	})
}

// sessionTokenTTL is how long the studio's auto-issued login PAT lives.
// Long enough to avoid daily re-login, short enough that a stolen cookie
// has bounded value. Long-lived CI tokens go through the explicit
// "Create token" UI which sets ExpiresAt=nil.
const sessionTokenTTL = 12 * time.Hour

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
	// A service principal has no password and must never hold a session: its
	// only credential is a capability token minted by a platform admin. The
	// refusal is indistinguishable from a wrong password so the login form
	// does not confirm which names are service identities.
	if user == nil || user.IsService() || !auth.CheckPassword(req.Password, user.PasswordHash) {
		httpError(w, "invalid credentials", http.StatusUnauthorized)
		return
	}

	// Issue a session-scope PAT. 12h TTL bounds blast radius if the cookie
	// leaks; "session" scope distinguishes it from long-lived CI tokens in
	// the token-list UI so users can spot and revoke active sessions.
	raw := auth.GenerateToken()
	now := time.Now()
	expiry := now.Add(sessionTokenTTL)
	token := &domain.AccessToken{
		TokenHash:   auth.HashToken(raw),
		TokenPrefix: auth.TokenPrefix(raw),
		UserID:      user.ID,
		Name:        "login",
		Scopes:      "session",
		CreatedAt:   &now,
		ExpiresAt:   &expiry,
	}

	if err := h.tokenStore.CreateToken(r.Context(), token); err != nil {
		httpError(w, "token creation failed", http.StatusInternalServerError)
		return
	}

	writeSessionCookie(w, raw, expiry)

	writeJSON(w, map[string]interface{}{
		// Token is also returned in the body so SDK / curl callers can
		// pin to header auth. Browser clients should rely on the cookie
		// and ignore this field.
		"token":     raw,
		"expiresAt": expiry,
		"user":      user,
	})
}

// Logout revokes the caller's current PAT and clears the session cookie.
// Returns 200 even if no token is present so client logout is idempotent.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if t := auth.GetToken(r.Context()); t != nil {
		_ = h.tokenStore.DeleteToken(r.Context(), t.TokenHash)
	}
	clearSessionCookie(w)
	writeJSON(w, map[string]string{"status": "ok"})
}

// writeSessionCookie sets the session PAT as an httpOnly cookie. The cookie
// is the studio's auth path; XSS in the React app cannot read it.
func writeSessionCookie(w http.ResponseWriter, raw string, expiry time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    raw,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		Secure:   true, // requires HTTPS in production; browsers ignore Secure on http://localhost during dev
		SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie tells the browser to drop the cookie immediately.
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, errNotAuthenticated, http.StatusUnauthorized)
		return
	}
	writeJSON(w, user)
}

func (h *AuthHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.userStore.FindAllUsers(r.Context())
	if err != nil {
		httpError(w, "failed to list users", http.StatusInternalServerError)
		return
	}
	writeJSON(w, users)
}

func (h *AuthHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		httpError(w, "username, email, and password are required", http.StatusBadRequest)
		return
	}
	if !auth.IsValidPlatformRole(req.Role) {
		httpError(w, "invalid role", http.StatusBadRequest)
		return
	}

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

// checkRegistration validates the submitted fields and refuses a name or
// address already in use. A service principal occupies its name too:
// registration can never take over a service identity.
func (h *AuthHandler) checkRegistration(ctx context.Context, username, email, password string) (int, string) {
	if username == "" || email == "" || password == "" {
		return http.StatusBadRequest, "username, email, and password are required"
	}
	if !isValidEmail(email) {
		return http.StatusBadRequest, "invalid email format"
	}
	if msg := isValidPassword(password); msg != "" {
		return http.StatusBadRequest, msg
	}
	if existing, _ := h.userStore.FindUserByUsername(ctx, username); existing != nil {
		return http.StatusConflict, "username already taken"
	}
	if existing, _ := h.userStore.FindUserByEmail(ctx, email); existing != nil {
		return http.StatusConflict, "email already registered"
	}
	return 0, ""
}

// checkRegistrationInvite validates an invite token before any account is
// created. Without one, only the first registration or open mode may proceed:
// an email that matches a pending invite grants nothing on its own.
func (h *AuthHandler) checkRegistrationInvite(ctx context.Context, token string, firstUser bool) (string, int, string) {
	if token == "" {
		if h.inviteOnly && !firstUser {
			return "", http.StatusForbidden, "registration is invite-only"
		}
		return "", 0, ""
	}
	if h.orgStore == nil {
		return "", http.StatusBadRequest, storage.ErrInviteInvalid.Error()
	}
	hash := hashToken(token)
	if _, err := h.orgStore.FindPendingInviteByToken(ctx, hash, time.Now()); err != nil {
		return "", inviteErrorStatus(err), inviteErrorMessage(err)
	}
	return hash, 0, ""
}

// joinInvitedOrg spends the invite for the account just created. If the token
// was spent in between, the account is removed again so a refused invite
// leaves nothing behind.
func (h *AuthHandler) joinInvitedOrg(ctx context.Context, hash string, user *domain.User) (int, string) {
	_, err := h.orgStore.AcceptPendingInvite(ctx, hash, user.ID, time.Now())
	if err == nil {
		return 0, ""
	}
	if derr := h.userStore.DeleteUser(ctx, user.ID); derr != nil {
		log.Printf("WARN: remove account %s after a refused invite: %v", user.ID, derr)
	}
	return inviteErrorStatus(err), inviteErrorMessage(err)
}

func (h *AuthHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userId")
	if err := h.userStore.DeleteUser(r.Context(), userID); err != nil {
		httpError(w, "delete failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
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
