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
	// UserID mints the token for another user. Platform admins only, and only
	// for a service principal — handing an admin a human's fresh secret would
	// be a silent credential grant.
	UserID string `json:"userId"`
	// Permissions turns the token into a capability token (EXC-365). Platform
	// admins only, and required when minting for a service principal.
	Permissions []string `json:"permissions"`
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
	raw, token, err := h.issueToken(r.Context(), req)
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
	userID, name, scopes, projectID string
	permissions                     []string
	lifetime                        auth.PATLifetime
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
	caller := auth.GetToken(r.Context())
	if err := restrictToCallerToken(caller, req.ProjectID, scopes); err != nil {
		httpError(w, err.message, err.status)
		return tokenSpec{}, false
	}
	if err := restrictCapabilityMint(caller, req); err != nil {
		httpError(w, err.message, err.status)
		return tokenSpec{}, false
	}
	spec := tokenSpec{userID: user.ID, name: req.Name, scopes: scopes, projectID: req.ProjectID, lifetime: lifetime}
	if err := h.resolveTokenSubject(r, user, req, &spec); err != nil {
		httpError(w, err.message, err.status)
		return tokenSpec{}, false
	}
	return spec, true
}

// refusal is a handler-level rejection carrying the status it maps to.
type refusal struct {
	status  int
	message string
}

func (e *refusal) Error() string { return e.message }

// restrictToCallerToken keeps a personal access token from minting a broader
// one (EXC-396). A PAT that could widen its own project binding or scopes
// would make the narrow token it was issued as meaningless — the CI credential
// bound to one project and to reads would be a full-account credential.
//
// PATs may still mint PATs: the documented install path mints the long-lived
// platform-services token with an operator PAT
// (docs/deployment/production-k8s-runbook.md §3.4). Subsetting keeps that
// working while closing the escalation.
//
// A session-authenticated request is the user acting directly and is
// unrestricted; so is a legacy all-purpose PAT, which carries no restriction
// to preserve. A request with no resolvable token is refused rather than
// waved through.
func restrictToCallerToken(caller *domain.AccessToken, projectID, scopes string) *refusal {
	if caller == nil {
		return &refusal{http.StatusForbidden, "the authenticating token could not be resolved"}
	}
	if auth.IsSessionToken(caller) {
		return nil
	}
	if !auth.ProjectBindingWithin(projectID, caller.ProjectID) {
		return &refusal{http.StatusForbidden, "a project-bound token may only mint tokens bound to the same project"}
	}
	if !auth.ScopesSubsetOf(scopes, caller.Scopes) {
		return &refusal{http.StatusForbidden, "a token may not mint a token with scopes it does not hold"}
	}
	return nil
}

// restrictCapabilityMint keeps a narrowed PAT from minting a service
// credential. A capability token carries platform permissions that no scope
// or project binding on the minting token bounds, so the subset rule above
// has nothing to subset: a CI PAT confined to reads on one project would
// still walk away with a token that reads every project's vault secrets.
// Only a session or an unrestricted token — the user acting with their full
// authority — may open that door, whatever their role allows.
func restrictCapabilityMint(caller *domain.AccessToken, req createTokenRequest) *refusal {
	if req.UserID == "" && len(req.Permissions) == 0 {
		return nil
	}
	if auth.IsUnrestrictedCredential(caller) {
		return nil
	}
	return &refusal{http.StatusForbidden, "a restricted token may not mint a service account token"}
}

// restrictCapabilityRotation applies the same rule to rotation, which returns
// a fresh secret for an existing token and is therefore a mint of the same
// authority under another name. A capability token may rotate itself — it
// gains nothing it did not already hold — and nothing else. (In production
// middleware.CapabilityGate refuses every non-GET a capability token makes,
// so today it cannot reach even self-rotation; this keeps the rule true if
// the gate ever widens.)
func restrictCapabilityRotation(caller, subject *domain.AccessToken) *refusal {
	if caller != nil && subject != nil && caller.TokenHash == subject.TokenHash {
		return nil
	}
	if auth.IsCapabilityToken(caller) {
		return &refusal{http.StatusForbidden, "a capability token may only rotate itself"}
	}
	if !auth.IsCapabilityToken(subject) {
		return nil
	}
	if auth.IsUnrestrictedCredential(caller) {
		return nil
	}
	return &refusal{http.StatusForbidden, "a restricted token may not rotate a service account token"}
}

// resolveTokenSubject applies the service-principal rules on top of a normal
// self-minted PAT: only a platform admin may name another user or attach a
// permission list, the named user must be a service principal, and a service
// principal's token must always carry a permission list — an unbounded token
// on a platform_admin service identity would be a full-platform credential.
func (h *AuthHandler) resolveTokenSubject(r *http.Request, caller *domain.User, req createTokenRequest, spec *tokenSpec) *refusal {
	if req.UserID == "" && len(req.Permissions) == 0 {
		return nil
	}
	if !auth.HasPermission(caller.Role, auth.PermManageUsers) {
		return &refusal{http.StatusForbidden, "only a platform admin may mint a token for a service account"}
	}
	if req.UserID == "" {
		return &refusal{http.StatusBadRequest, "permissions require userId naming a service account"}
	}
	subject, _ := h.userStore.FindUserByID(r.Context(), req.UserID)
	if subject == nil || !subject.IsService() {
		return &refusal{http.StatusBadRequest, "userId must name a service account"}
	}
	permissions, err := auth.NormalizeCapabilities(req.Permissions)
	if err != nil {
		return &refusal{http.StatusBadRequest, err.Error()}
	}
	if len(permissions) == 0 {
		return &refusal{http.StatusBadRequest, "a service account token requires at least one permission"}
	}
	spec.userID = subject.ID
	spec.permissions = permissions
	return nil
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
func (h *AuthHandler) issueToken(ctx context.Context, spec tokenSpec) (string, *domain.AccessToken, error) {
	raw := auth.GenerateToken()
	now := time.Now()
	token := &domain.AccessToken{
		TokenHash:   auth.HashToken(raw),
		TokenPrefix: auth.TokenPrefix(raw),
		UserID:      spec.userID,
		Name:        spec.name,
		Scopes:      spec.scopes,
		ProjectID:   spec.projectID,
		Permissions: spec.permissions,
		CreatedAt:   &now,
		ExpiresAt:   spec.lifetime.ExpiryFrom(now),
	}
	if err := h.tokenStore.CreateToken(ctx, token); err != nil {
		return "", nil, err
	}
	return raw, token, nil
}

func issuedTokenResponse(raw string, token *domain.AccessToken) map[string]interface{} {
	return map[string]interface{}{
		"token":       raw, // returned once
		"prefix":      token.TokenPrefix,
		"name":        token.Name,
		"scopes":      token.Scopes,
		"projectId":   token.ProjectID,
		"permissions": token.Permissions,
		"expiresAt":   token.ExpiresAt,
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
	// Rotation returns a fresh secret for tok, so the same rules that govern
	// creation govern this door too.
	caller := auth.GetToken(r.Context())
	if err := restrictToCallerToken(caller, tok.ProjectID, tok.Scopes); err != nil {
		httpError(w, err.message, err.status)
		return
	}
	if err := restrictCapabilityRotation(caller, tok); err != nil {
		httpError(w, err.message, err.status)
		return
	}
	grace, ok := decodeGrace(w, r)
	if !ok {
		return
	}
	raw, fresh, err := h.issueToken(r.Context(), tokenSpec{
		userID:      tok.UserID,
		name:        tok.Name,
		scopes:      tok.Scopes,
		projectID:   tok.ProjectID,
		permissions: tok.Permissions,
		lifetime:    auth.LifetimeOf(tok),
	})
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
	admin := (allowAdmin || h.ownedByService(r.Context(), tok)) && auth.HasPermission(caller.Role, auth.PermManageUsers)
	if !owner && !admin {
		httpError(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return tok, true
}

// ownedByService reports whether the token belongs to a service principal.
// Rotating one hands the caller a fresh secret, which is the point for a
// service identity (the rotation job is an admin) but would be a silent
// credential grant for a human — so admins may rotate only these.
func (h *AuthHandler) ownedByService(ctx context.Context, tok *domain.AccessToken) bool {
	if h.userStore == nil {
		return false
	}
	owner, _ := h.userStore.FindUserByID(ctx, tok.UserID)
	return owner.IsService()
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
