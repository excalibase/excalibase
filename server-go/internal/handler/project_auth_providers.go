package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	auditActionAuthProviderUpdate = "auth_provider.update"
	auditActionAuthProviderDelete = "auth_provider.delete"
	auditResourceAuthProvider     = "auth_provider"
	auditActionAuthSettingsUpdate = "auth_settings.update"
	auditResourceAuthSettings     = "auth_settings"
	authProviderSecretField       = "client_secret"
	errAuthProvidersUnavailable   = "sign-in providers are not available"
	errAuthProvidersRead          = "failed to read the sign-in providers"
)

// authProviderSecrets reads, writes and removes a project's auth secrets; the vault client satisfies it.
type authProviderSecrets interface {
	Get(path string) (map[string]string, error)
	Put(path string, data map[string]string) error
	PutIfAbsent(path string, data map[string]string) (bool, error)
	Delete(path string) error
}

// AuthOAuthDeps wires end-user OAuth sign-in (EXC-662). Nil stores leave the
// provider API unavailable and /info reporting nothing switched on.
type AuthOAuthDeps struct {
	Flags     features.Flags
	Providers storage.ProjectAuthProviderStore
	SignIn    storage.ProjectAuthSignInStore
	// MFA writes the end-user MFA switch behind the authmfa flag.
	MFA     storage.ProjectAuthMFAStore
	Secrets authProviderSecrets
	// PublicBaseURL is the API host the auth service's callbacks are served behind.
	PublicBaseURL string
	Audit         auditWriter
}

// SetAuthOAuth wires the end-user OAuth settings.
func (h *ProvisioningHandler) SetAuthOAuth(deps AuthOAuthDeps) { h.authOAuth = deps }

func (h *ProvisioningHandler) authOAuthOn(ctx context.Context) bool {
	return features.Enabled(ctx, h.authOAuth.Flags, features.AuthOAuth)
}

func (h *ProvisioningHandler) authOTPOn(ctx context.Context) bool {
	return features.Enabled(ctx, h.authOAuth.Flags, features.AuthOTP)
}

// userAPIForInfo is /info's userApiEnabled: true while authuser is on, else absent.
func (h *ProvisioningHandler) userAPIForInfo(ctx context.Context) *bool {
	if !features.Enabled(ctx, h.authOAuth.Flags, features.AuthUser) {
		return nil
	}
	on := true
	return &on
}

func (h *ProvisioningHandler) authGuestOn(ctx context.Context) bool {
	return features.Enabled(ctx, h.authOAuth.Flags, features.AuthGuest)
}

// stableSubjectForInfo is /info's stableSubject: true while authguest is on, else absent.
func (h *ProvisioningHandler) stableSubjectForInfo(ctx context.Context) *bool {
	if !h.authGuestOn(ctx) {
		return nil
	}
	on := true
	return &on
}

// signInPolicyOn is whether any end-user sign-in method that reads the
// redirect list or the new-user switch is on.
func (h *ProvisioningHandler) signInPolicyOn(ctx context.Context) bool {
	return h.authOAuthOn(ctx) || h.authOTPOn(ctx) || h.authGuestOn(ctx)
}

// authProviderView is one provider on the wire; the secret is never here.
type authProviderView struct {
	Provider        string `json:"provider"`
	Enabled         bool   `json:"enabled"`
	ClientID        string `json:"clientId"`
	ClientSecretSet bool   `json:"clientSecretSet"`
	CallbackURL     string `json:"callbackUrl"`
}

type authProviderUpdate struct {
	Enabled      bool    `json:"enabled"`
	ClientID     string  `json:"clientId"`
	ClientSecret *string `json:"clientSecret"`
}

// GetAuthProviders serves GET /api/projects/{projectId}/auth-providers: both
// providers, in order, with the callback URL to register at each.
func (h *ProvisioningHandler) GetAuthProviders(w http.ResponseWriter, r *http.Request) {
	projectID, orgSlug, ok := h.authProvidersProject(w, r)
	if !ok {
		return
	}
	stored, err := h.authOAuth.Providers.ListAuthProviders(r.Context(), projectID)
	if err != nil {
		log.Printf("ERROR: read the sign-in providers of project %s: %v", projectID, err)
		httpError(w, errAuthProvidersRead, http.StatusInternalServerError)
		return
	}
	views := make([]authProviderView, 0, len(domain.AuthProviderNames()))
	for _, provider := range domain.AllAuthProviders(stored) {
		views = append(views, h.authProviderViewFor(provider, orgSlug, projectID))
	}
	writeJSON(w, map[string]any{"providers": views})
}

// PutAuthProvider serves PUT /api/projects/{projectId}/auth-providers/{provider}.
// The secret goes to the vault before the row says one is set, so a row never
// claims a secret the vault does not hold.
func (h *ProvisioningHandler) PutAuthProvider(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	projectID, orgSlug, ok := h.authProvidersProject(w, r)
	if !ok {
		return
	}
	if !domain.IsAuthProvider(provider) {
		httpError(w, "unknown sign-in provider", http.StatusNotFound)
		return
	}
	next, secret, ok := h.decodeAuthProviderUpdate(w, r, projectID, provider)
	if !ok {
		return
	}
	if secret != "" && !h.storeAuthProviderSecret(w, projectID, provider, secret) {
		return
	}
	if err := h.authOAuth.Providers.PutAuthProvider(r.Context(), projectID, next); err != nil {
		log.Printf("ERROR: save the %s provider of project %s: %v", provider, projectID, err)
		httpError(w, "failed to save the provider", http.StatusInternalServerError)
		return
	}
	h.recordAuthProvider(r, projectID, next, secret != "")
	writeJSON(w, h.authProviderViewFor(next, orgSlug, projectID))
}

// DeleteAuthProvider serves DELETE /api/projects/{projectId}/auth-providers/{provider}.
// The row goes first, switching the provider off, so a row never claims a
// secret the vault no longer holds; both steps are safe to repeat.
func (h *ProvisioningHandler) DeleteAuthProvider(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	projectID, orgSlug, ok := h.authProvidersProject(w, r)
	if !ok {
		return
	}
	if !domain.IsAuthProvider(provider) {
		httpError(w, "unknown sign-in provider", http.StatusNotFound)
		return
	}
	if h.authOAuth.Secrets == nil {
		httpError(w, "the vault is not available", http.StatusServiceUnavailable)
		return
	}
	if err := h.authOAuth.Providers.DeleteAuthProvider(r.Context(), projectID, provider); err != nil {
		log.Printf("ERROR: delete the %s provider of project %s: %v", provider, projectID, err)
		httpError(w, "failed to delete the provider", http.StatusInternalServerError)
		return
	}
	if err := h.authOAuth.Secrets.Delete(domain.AuthProviderSecretPath(projectID, provider)); err != nil {
		log.Printf("ERROR: delete the %s client secret of project %s: %v", provider, projectID, err)
		httpError(w, "failed to delete the client secret", http.StatusInternalServerError)
		return
	}
	h.writeAuthAudit(r, projectID, auditActionAuthProviderDelete, auditResourceAuthProvider, provider, `{"secretRemoved":true}`)
	writeJSON(w, h.authProviderViewFor(domain.ProjectAuthProvider{Provider: provider}, orgSlug, projectID))
}

// decodeAuthProviderUpdate validates the body against the stored row and
// returns the row to write and the new secret ("" keeps the stored one).
func (h *ProvisioningHandler) decodeAuthProviderUpdate(w http.ResponseWriter, r *http.Request, projectID, provider string) (domain.ProjectAuthProvider, string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var body authProviderUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return domain.ProjectAuthProvider{}, "", false
	}
	clientID, err := domain.ValidateAuthProviderClientID(body.ClientID)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return domain.ProjectAuthProvider{}, "", false
	}
	secret := ""
	if body.ClientSecret != nil && *body.ClientSecret != "" {
		if err := domain.ValidateAuthProviderClientSecret(*body.ClientSecret); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return domain.ProjectAuthProvider{}, "", false
		}
		secret = *body.ClientSecret
	}
	current, err := h.storedAuthProvider(r.Context(), projectID, provider)
	if err != nil {
		log.Printf("ERROR: read the %s provider of project %s: %v", provider, projectID, err)
		httpError(w, errAuthProvidersRead, http.StatusInternalServerError)
		return domain.ProjectAuthProvider{}, "", false
	}
	secretSet := current.ClientSecretSet || secret != ""
	if body.Enabled && (clientID == "" || !secretSet) {
		httpError(w, "enabling a provider needs its client id and client secret", http.StatusBadRequest)
		return domain.ProjectAuthProvider{}, "", false
	}
	next := domain.ProjectAuthProvider{Provider: provider, Enabled: body.Enabled, ClientID: clientID, ClientSecretSet: secretSet}
	return next, secret, true
}

func (h *ProvisioningHandler) storeAuthProviderSecret(w http.ResponseWriter, projectID, provider, secret string) bool {
	if h.authOAuth.Secrets == nil {
		httpError(w, "the vault is not available", http.StatusServiceUnavailable)
		return false
	}
	path := domain.AuthProviderSecretPath(projectID, provider)
	if err := h.authOAuth.Secrets.Put(path, map[string]string{authProviderSecretField: secret}); err != nil {
		log.Printf("ERROR: store the %s client secret of project %s: %v", provider, projectID, err)
		httpError(w, "failed to store the client secret", http.StatusInternalServerError)
		return false
	}
	return true
}

func (h *ProvisioningHandler) storedAuthProvider(ctx context.Context, projectID, provider string) (domain.ProjectAuthProvider, error) {
	stored, err := h.authOAuth.Providers.ListAuthProviders(ctx, projectID)
	if err != nil {
		return domain.ProjectAuthProvider{}, err
	}
	for _, row := range domain.AllAuthProviders(stored) {
		if row.Provider == provider {
			return row, nil
		}
	}
	return domain.ProjectAuthProvider{}, errors.New("unknown sign-in provider")
}

// authProvidersProject resolves the path project and its org slug, writing
// the error response itself when either fails.
func (h *ProvisioningHandler) authProvidersProject(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", "", false
	}
	if h.authOAuth.Providers == nil {
		httpError(w, errAuthProvidersUnavailable, http.StatusServiceUnavailable)
		return "", "", false
	}
	inst, err := h.svc.GetInstance(projectID)
	if err != nil {
		httpError(w, "project not found", http.StatusNotFound)
		return "", "", false
	}
	org, ok := h.resolveInfoOrg(w, r, inst.OrgID)
	if !ok {
		return "", "", false
	}
	return projectID, org.Slug, true
}

func (h *ProvisioningHandler) authProviderViewFor(provider domain.ProjectAuthProvider, orgSlug, projectID string) authProviderView {
	return authProviderView{
		Provider:        provider.Provider,
		Enabled:         provider.Enabled,
		ClientID:        provider.ClientID,
		ClientSecretSet: provider.ClientSecretSet,
		CallbackURL:     domain.AuthProviderCallbackURL(h.authOAuth.PublicBaseURL, orgSlug, projectID, provider.Provider),
	}
}

// recordAuthProvider audits the change best-effort; the secret is never in it.
func (h *ProvisioningHandler) recordAuthProvider(r *http.Request, projectID string, saved domain.ProjectAuthProvider, secretChanged bool) {
	details, _ := json.Marshal(map[string]any{"enabled": saved.Enabled, "clientId": saved.ClientID, "secretChanged": secretChanged})
	h.writeAuthAudit(r, projectID, auditActionAuthProviderUpdate, auditResourceAuthProvider, saved.Provider, string(details))
}

// writeAuthAudit records a sign-in settings change best-effort.
func (h *ProvisioningHandler) writeAuthAudit(r *http.Request, projectID, action, resource, resourceID, details string) {
	if h.authOAuth.Audit == nil {
		return
	}
	now := time.Now()
	entry := &domain.AuditEntry{
		Action: action, Resource: resource, ResourceID: resourceID,
		ProjectID: projectID, Details: details, IPAddress: clientIP(r), Timestamp: &now,
	}
	if user := auth.GetUser(r.Context()); user != nil {
		entry.UserID = user.ID
	}
	if err := h.authOAuth.Audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s %s/%s: %v", action, projectID, resourceID, err)
	}
}
