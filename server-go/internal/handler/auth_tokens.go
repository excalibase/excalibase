package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/go-chi/chi/v5"
)

const (
	auditActionTokenRotate  = "token.rotate"
	auditResourceToken      = "access_token"
	maxRotationGraceSeconds = 3600
	errTokenNotFound        = "token not found"
)

// createTokenRequest is the body of POST /api/auth/tokens. ProjectID and
// Scopes are optional: omitted = an unbound, all-purpose PAT (EXC-323).
type createTokenRequest struct {
	Name string `json:"name"`
	// ExpiresIn is "30d", "12h", "never" or empty (default 90d).
	ExpiresIn string   `json:"expiresIn"`
	ProjectID string   `json:"projectId"`
	Scopes    []string `json:"scopes"`
}

type rotateTokenRequest struct {
	// GraceSeconds keeps the old secret valid for a short overlap so callers
	// can swap credentials without a hard cut. 0 (default) revokes at once.
	GraceSeconds int `json:"graceSeconds"`
}

func (h *AuthHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, errNotAuthenticated, http.StatusUnauthorized)
		return
	}
	tokens, err := h.tokenStore.ListTokensByUser(r.Context(), user.ID)
	if err != nil {
		httpError(w, "failed to list tokens", http.StatusInternalServerError)
		return
	}
	writeJSON(w, tokens)
}

func (h *AuthHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, errNotAuthenticated, http.StatusUnauthorized)
		return
	}
	req, ok := h.decodeCreateToken(w, r, user)
	if !ok {
		return
	}
	raw, token, err := h.issueToken(r.Context(), user.ID, req.name, req.scopes, req.projectID, req.lifetime)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, issuedTokenResponse(raw, token))
}

// tokenSpec is a validated createTokenRequest: canonical scopes, parsed
// lifetime and a project the caller is allowed to bind to.
type tokenSpec struct {
	name, scopes, projectID string
	lifetime                auth.PATLifetime
}

// decodeCreateToken parses and validates a token-creation body. On refusal
// it writes the response and returns false: 400 for a malformed body, scope
// or lifetime, 404 when the project to bind to is not visible to the caller.
func (h *AuthHandler) decodeCreateToken(w http.ResponseWriter, r *http.Request, user *domain.User) (tokenSpec, bool) {
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return tokenSpec{}, false
	}
	if req.Name == "" {
		httpError(w, "token name is required", http.StatusBadRequest)
		return tokenSpec{}, false
	}
	scopes, err := auth.NormalizeScopes(req.Scopes)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return tokenSpec{}, false
	}
	lifetime, err := auth.ParseExpiresIn(req.ExpiresIn)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return tokenSpec{}, false
	}
	if req.ProjectID != "" && !h.callerCanSeeProject(r, user, req.ProjectID) {
		httpError(w, "project not found", http.StatusNotFound)
		return tokenSpec{}, false
	}
	return tokenSpec{name: req.Name, scopes: scopes, projectID: req.ProjectID, lifetime: lifetime}, true
}

// callerCanSeeProject applies the same binding as the project routes: a PAT
// may only be bound to a project the caller (and the caller's current token)
// can reach. Fails closed when the stores are not wired.
func (h *AuthHandler) callerCanSeeProject(r *http.Request, user *domain.User, projectID string) bool {
	if h.instanceStore == nil || h.orgStore == nil {
		return false
	}
	ctx := r.Context()
	return custommw.ResolveProjectAccess(ctx, user, auth.GetToken(ctx), projectID, h.instanceStore, h.orgStore) != nil
}

// issueToken mints and persists a PAT bound to userID (and, when projectID
// is set, confined to that project). The raw secret is returned exactly once
// and never stored.
func (h *AuthHandler) issueToken(ctx context.Context, userID, name, scopes, projectID string, lifetime auth.PATLifetime) (string, *domain.AccessToken, error) {
	raw := auth.GenerateToken()
	now := time.Now()
	token := &domain.AccessToken{
		TokenHash:   auth.HashToken(raw),
		TokenPrefix: auth.TokenPrefix(raw),
		UserID:      userID,
		Name:        name,
		Scopes:      scopes,
		ProjectID:   projectID,
		CreatedAt:   &now,
		ExpiresAt:   lifetime.ExpiryFrom(now),
	}
	if err := h.tokenStore.CreateToken(ctx, token); err != nil {
		return "", nil, err
	}
	return raw, token, nil
}

func issuedTokenResponse(raw string, token *domain.AccessToken) map[string]interface{} {
	return map[string]interface{}{
		"token":     raw, // returned once
		"prefix":    token.TokenPrefix,
		"name":      token.Name,
		"scopes":    token.Scopes,
		"projectId": token.ProjectID,
		"expiresAt": token.ExpiresAt,
	}
}

func (h *AuthHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	// Platform admins (PermManageUsers) may revoke any token — supports
	// incident response.
	tok, ok := h.tokenForCaller(w, r, true)
	if !ok {
		return
	}
	if err := h.tokenStore.DeleteToken(r.Context(), tok.TokenHash); err != nil {
		httpError(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "revoked"})
}

// RotateToken re-issues the PAT named by {tokenHash} with the same owner,
// name, scopes, project binding and lifetime (measured from now), then retires the old
// secret — immediately, or after graceSeconds (max 3600). Only the owner may
// rotate: handing an admin another user's fresh secret would be a
// credential grant, which revoke-then-reissue already covers explicitly.
func (h *AuthHandler) RotateToken(w http.ResponseWriter, r *http.Request) {
	tok, ok := h.tokenForCaller(w, r, false)
	if !ok {
		return
	}
	grace, ok := decodeGrace(w, r)
	if !ok {
		return
	}
	raw, fresh, err := h.issueToken(r.Context(), tok.UserID, tok.Name, tok.Scopes, tok.ProjectID, auth.LifetimeOf(tok))
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	previousExpiry, err := h.retireToken(r.Context(), tok, grace)
	if err != nil {
		httpError(w, "rotation incomplete: old token not retired", http.StatusInternalServerError)
		return
	}
	h.auditRotation(r, tok, fresh, grace)

	resp := issuedTokenResponse(raw, fresh)
	resp["previousExpiresAt"] = previousExpiry
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, resp)
}

// tokenForCaller resolves {tokenHash} and enforces ownership. It writes the
// error response itself and returns ok=false when the caller may not act.
func (h *AuthHandler) tokenForCaller(w http.ResponseWriter, r *http.Request, allowAdmin bool) (*domain.AccessToken, bool) {
	caller := auth.GetUser(r.Context())
	if caller == nil {
		httpError(w, errNotAuthenticated, http.StatusUnauthorized)
		return nil, false
	}
	tok, err := h.tokenStore.FindByTokenHash(r.Context(), chi.URLParam(r, "tokenHash"))
	if err != nil || tok == nil {
		httpError(w, errTokenNotFound, http.StatusNotFound)
		return nil, false
	}
	owner := tok.UserID == caller.ID
	admin := allowAdmin && auth.HasPermission(caller.Role, auth.PermManageUsers)
	if !owner && !admin {
		httpError(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return tok, true
}

// decodeGrace reads an optional JSON body; an empty body means no grace.
func decodeGrace(w http.ResponseWriter, r *http.Request) (time.Duration, bool) {
	var req rotateTokenRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, "invalid request body", http.StatusBadRequest)
			return 0, false
		}
	}
	if req.GraceSeconds < 0 || req.GraceSeconds > maxRotationGraceSeconds {
		httpError(w, "graceSeconds must be between 0 and 3600", http.StatusBadRequest)
		return 0, false
	}
	return time.Duration(req.GraceSeconds) * time.Second, true
}

// retireToken deletes the old token, or shortens it to the grace window.
// The window never extends an expiry that was already sooner.
func (h *AuthHandler) retireToken(ctx context.Context, old *domain.AccessToken, grace time.Duration) (*time.Time, error) {
	if grace == 0 {
		return nil, h.tokenStore.DeleteToken(ctx, old.TokenHash)
	}
	until := time.Now().Add(grace)
	if old.ExpiresAt != nil && old.ExpiresAt.Before(until) {
		until = *old.ExpiresAt
	}
	return &until, h.tokenStore.UpdateTokenExpiry(ctx, old.TokenHash, &until)
}

// auditRotation records the rotation best-effort. Only display prefixes are
// written — never a hash or raw secret.
func (h *AuthHandler) auditRotation(r *http.Request, old, fresh *domain.AccessToken, grace time.Duration) {
	if h.auditLog == nil {
		return
	}
	details, _ := json.Marshal(map[string]interface{}{
		"previousPrefix": old.TokenPrefix,
		"newPrefix":      fresh.TokenPrefix,
		"graceSeconds":   int(grace / time.Second),
	})
	now := time.Now()
	_ = h.auditLog.LogAudit(r.Context(), &domain.AuditEntry{
		UserID:     old.UserID,
		Action:     auditActionTokenRotate,
		Resource:   auditResourceToken,
		ResourceID: old.TokenPrefix,
		Details:    string(details),
		IPAddress:  clientIP(r),
		Timestamp:  &now,
	})
}
