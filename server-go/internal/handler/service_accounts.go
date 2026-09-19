package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// Service principals (EXC-365) are the identities the platform's own
// components authenticate as — svc-auth for the auth service, svc-graphql for
// the engine. They exist to own capability tokens: they have no password, are
// refused by /login and /register, and are never added to an organization.
//
// Their platform role is platform_admin because the routes they call
// (/api/vault/secrets, /api/projects/{id}/info, the policy reads) are gated on
// platform permissions and on project membership that a service has none of.
// The role is NOT what bounds them: every token a service principal owns is a
// capability token, and middleware.CapabilityGate refuses every request its
// permission list does not name. Minting a permission-less token for a service
// principal is therefore rejected outright.
const (
	serviceAccountRole  = "platform_admin"
	serviceAccountEmail = "@svc.excalibase.internal"

	errServiceAccountNotFound = "service account not found"
	auditResourceServiceAcct  = "service_account"
)

// serviceAccountName accepts a short lowercase slug: these names end up in
// Secret keys, env var names and audit records.
var serviceAccountName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

// ServiceAccountHandler exposes the platform-admin surface for service
// principals: create (idempotent by name), list, inspect their tokens and
// delete. Token minting itself stays on POST /api/auth/tokens so there is one
// place that issues credentials.
type ServiceAccountHandler struct {
	userStore  storage.UserStore
	tokenStore storage.TokenStore
	auditLog   auditWriter // optional
}

func NewServiceAccountHandler(userStore storage.UserStore, tokenStore storage.TokenStore, auditLog auditWriter) *ServiceAccountHandler {
	return &ServiceAccountHandler{userStore: userStore, tokenStore: tokenStore, auditLog: auditLog}
}

// Routes mounts the subtree. The caller applies auth.RequireAuth; every route
// here additionally requires the platform user-management permission, and the
// two that change which machine identities exist require an unrestricted
// credential on top — a narrowed PAT must not be able to register the
// principal a capability token would then be minted for (EXC-396).
func (h *ServiceAccountHandler) Routes(r chi.Router) {
	r.Use(auth.RequirePermission(auth.PermManageUsers))
	r.Get("/", h.List)
	r.Get("/{name}/tokens", h.ListTokens)
	r.With(auth.RequireUnrestrictedCredential).Post("/", h.Create)
	r.With(auth.RequireUnrestrictedCredential).Delete("/{name}", h.Delete)
}

// Create registers a service principal, or returns the existing one unchanged
// when the name is already taken by a service principal. Idempotency is what
// lets the bootstrap Job run on every upgrade without special-casing re-runs.
func (h *ServiceAccountHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	if !serviceAccountName.MatchString(name) {
		httpError(w, "name must be a lowercase slug of 2-40 characters", http.StatusBadRequest)
		return
	}
	existing, _ := h.userStore.FindUserByUsername(r.Context(), name)
	if existing != nil {
		if !existing.IsService() {
			httpError(w, "name is already taken by a user account", http.StatusConflict)
			return
		}
		writeJSON(w, existing)
		return
	}
	user, err := h.persist(r.Context(), name)
	if err != nil {
		httpError(w, "failed to create service account", http.StatusInternalServerError)
		return
	}
	h.audit(r, "service_account.create", user)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, user)
}

// persist writes the new principal. The password hash is left empty, which no
// bcrypt comparison can ever match — a service principal has no password to
// leak or guess.
func (h *ServiceAccountHandler) persist(ctx context.Context, name string) (*domain.User, error) {
	now := time.Now()
	user := &domain.User{
		ID:        auth.GenerateID(),
		Username:  name,
		Email:     name + serviceAccountEmail,
		Role:      serviceAccountRole,
		Active:    true,
		Kind:      domain.UserKindService,
		CreatedAt: &now,
	}
	if err := h.userStore.CreateUser(ctx, user); err != nil {
		return nil, err
	}
	return user, nil
}

// List returns every service principal. Human accounts are listed by
// /api/auth/users; keeping the two apart stops an operator scrolling past a
// service identity in a user list.
func (h *ServiceAccountHandler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.userStore.FindAllUsers(r.Context())
	if err != nil {
		httpError(w, "failed to list service accounts", http.StatusInternalServerError)
		return
	}
	services := make([]*domain.User, 0, len(users))
	for _, u := range users {
		if u.IsService() {
			services = append(services, u)
		}
	}
	writeJSON(w, services)
}

// ListTokens returns the metadata of the principal's tokens — never a secret.
// The rotation CronJob reads it to learn which token hash to rotate.
func (h *ServiceAccountHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	user, ok := h.resolve(w, r)
	if !ok {
		return
	}
	tokens, err := h.tokenStore.ListTokensByUser(r.Context(), user.ID)
	if err != nil {
		httpError(w, "failed to list tokens", http.StatusInternalServerError)
		return
	}
	writeJSON(w, serviceTokenViews(tokens))
}

// serviceTokenView is the metadata of one service token. TokenHash is the
// route identifier for revoke/rotate; it is a one-way digest, never a secret.
type serviceTokenView struct {
	TokenHash   string     `json:"tokenHash"`
	TokenPrefix string     `json:"tokenPrefix"`
	Name        string     `json:"name"`
	Permissions []string   `json:"permissions"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsed    *time.Time `json:"lastUsed,omitempty"`
}

func serviceTokenViews(tokens []*domain.AccessToken) []serviceTokenView {
	views := make([]serviceTokenView, 0, len(tokens))
	for _, t := range tokens {
		views = append(views, serviceTokenView{
			TokenHash:   t.TokenHash,
			TokenPrefix: t.TokenPrefix,
			Name:        t.Name,
			Permissions: t.Permissions,
			ExpiresAt:   t.ExpiresAt,
			LastUsed:    t.LastUsed,
		})
	}
	return views
}

// Delete removes the principal and every token it owns, so deleting a service
// identity actually revokes its credentials.
func (h *ServiceAccountHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := h.resolve(w, r)
	if !ok {
		return
	}
	tokens, err := h.tokenStore.ListTokensByUser(r.Context(), user.ID)
	if err != nil {
		httpError(w, "failed to revoke tokens", http.StatusInternalServerError)
		return
	}
	for _, t := range tokens {
		if err := h.tokenStore.DeleteToken(r.Context(), t.TokenHash); err != nil {
			httpError(w, "failed to revoke tokens", http.StatusInternalServerError)
			return
		}
	}
	if err := h.userStore.DeleteUser(r.Context(), user.ID); err != nil {
		httpError(w, "failed to delete service account", http.StatusInternalServerError)
		return
	}
	h.audit(r, "service_account.delete", user)
	writeJSON(w, map[string]string{"status": "deleted"})
}

// resolve looks up {name} and refuses anything that is not a service
// principal, so this subtree can never act on a human account.
func (h *ServiceAccountHandler) resolve(w http.ResponseWriter, r *http.Request) (*domain.User, bool) {
	user, _ := h.userStore.FindUserByUsername(r.Context(), chi.URLParam(r, "name"))
	if user == nil || !user.IsService() {
		httpError(w, errServiceAccountNotFound, http.StatusNotFound)
		return nil, false
	}
	return user, true
}

// audit records the change best-effort; the principal's name is the platform's
// own constant, never caller-supplied free text.
func (h *ServiceAccountHandler) audit(r *http.Request, action string, user *domain.User) {
	if h.auditLog == nil {
		return
	}
	actor := auth.GetUser(r.Context())
	now := time.Now()
	entry := &domain.AuditEntry{
		Action:     action,
		Resource:   auditResourceServiceAcct,
		ResourceID: user.ID,
		IPAddress:  clientIP(r),
		Timestamp:  &now,
	}
	if actor != nil {
		entry.UserID = actor.ID
	}
	_ = h.auditLog.LogAudit(r.Context(), entry)
}
