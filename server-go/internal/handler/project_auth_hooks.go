package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

const (
	auditActionAuthHookUpdate       = "auth_hook.update"
	auditActionAuthHookSecretRotate = "auth_hook_secret.rotate"
	auditActionAuthHookSecretDelete = "auth_hook_secret.delete"
	auditResourceAuthHook           = "auth_hook"
	authHookSecretField             = "secret"
	errAuthHooksRead                = "failed to read the auth hooks"
	errAuthHooksSave                = "failed to save the auth hooks"
)

// authHookView is one hook on the wire.
type authHookView struct {
	Hook    string `json:"hook"`
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
}

// authHooksResponse is the GET body, and what every write answers.
type authHooksResponse struct {
	SecretSet bool           `json:"secretSet"`
	Hooks     []authHookView `json:"hooks"`
}

type authHookUpdate struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
}

func (h *ProvisioningHandler) authHooksOn(ctx context.Context) bool {
	return features.Enabled(ctx, h.authOAuth.Flags, features.AuthHooks)
}

// GetAuthHooks serves GET /api/projects/{projectId}/auth-hooks: all three
// hooks in order, and whether the signing secret is set (never the secret).
func (h *ProvisioningHandler) GetAuthHooks(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authHooksProject(w, r)
	if !ok {
		return
	}
	h.writeAuthHooks(r.Context(), w, projectID)
}

// PutAuthHook serves PUT /api/projects/{projectId}/auth-hooks/{hook}.
func (h *ProvisioningHandler) PutAuthHook(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authHooksProject(w, r)
	if !ok {
		return
	}
	hook := chi.URLParam(r, "hook")
	if !domain.IsAuthHook(hook) {
		httpError(w, "unknown auth hook", http.StatusNotFound)
		return
	}
	next, ok := decodeAuthHookUpdate(w, r, hook)
	if !ok {
		return
	}
	err := h.authOAuth.Hooks.PutAuthHook(r.Context(), projectID, next)
	if errors.Is(err, domain.ErrAuthHookNeedsSecret) {
		httpError(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		log.Printf("ERROR: save the %s hook of project %s: %v", hook, projectID, err)
		httpError(w, errAuthHooksSave, http.StatusInternalServerError)
		return
	}
	details, _ := json.Marshal(map[string]any{"hook": hook, "enabled": next.Enabled, "host": domain.AuthHookURLHost(next.URL)})
	h.writeAuthAudit(r, projectID, auditActionAuthHookUpdate, auditResourceAuthHook, hook, string(details))
	h.writeAuthHooks(r.Context(), w, projectID)
}

// decodeAuthHookUpdate validates the body. The 409 for a missing secret is the
// store's, so the check cannot race a removal.
func decodeAuthHookUpdate(w http.ResponseWriter, r *http.Request, hook string) (domain.ProjectAuthHook, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body authHookUpdate
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return domain.ProjectAuthHook{}, false
	}
	next := domain.ProjectAuthHook{Hook: hook, Enabled: body.Enabled}
	if body.URL == "" {
		if body.Enabled {
			httpError(w, "enabling a hook needs its URL", http.StatusBadRequest)
			return domain.ProjectAuthHook{}, false
		}
		return next, true
	}
	normalized, err := domain.ValidateAuthHookURL(body.URL)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return domain.ProjectAuthHook{}, false
	}
	next.URL = normalized
	return next, true
}

// RotateAuthHookSecret serves POST /api/projects/{projectId}/auth-hooks/secret:
// a fresh secret replaces the old one at once and is answered this one time.
// The vault write comes first, so the store never claims a secret auth cannot read.
func (h *ProvisioningHandler) RotateAuthHookSecret(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authHooksSecretProject(w, r)
	if !ok {
		return
	}
	release, ok := h.lockAuthHookSecret(r.Context(), w, projectID)
	if !ok {
		return
	}
	defer release()
	wasSet, err := h.authOAuth.Hooks.AuthHookSecretSet(r.Context(), projectID)
	if err != nil {
		log.Printf("ERROR: read the hook secret state of project %s: %v", projectID, err)
		httpError(w, errAuthHooksRead, http.StatusInternalServerError)
		return
	}
	secret, ok := h.storeNewAuthHookSecret(w, projectID)
	if !ok {
		return
	}
	if err := h.authOAuth.Hooks.MarkAuthHookSecretSet(r.Context(), projectID); err != nil {
		log.Printf("ERROR: mark the hook secret of project %s: %v", projectID, err)
		// A rotation already replaced the old secret: answering the new one keeps the hooks verifiable.
		if !wasSet {
			httpError(w, errAuthHooksSave, http.StatusInternalServerError)
			return
		}
	}
	h.writeAuthAudit(r, projectID, auditActionAuthHookSecretRotate, auditResourceAuthHook, projectID, `{"secretSet":true}`)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]string{"secret": secret})
}

func (h *ProvisioningHandler) storeNewAuthHookSecret(w http.ResponseWriter, projectID string) (string, bool) {
	secret, err := domain.GenerateAuthHookSecret()
	if err != nil {
		log.Printf("ERROR: generate the hook secret of project %s: %v", projectID, err)
		httpError(w, "failed to generate the secret", http.StatusInternalServerError)
		return "", false
	}
	if err := h.authOAuth.Secrets.Put(domain.AuthHooksSecretPath(projectID), map[string]string{authHookSecretField: secret}); err != nil {
		log.Printf("ERROR: store the hook secret of project %s: %v", projectID, err)
		httpError(w, "failed to store the secret", http.StatusInternalServerError)
		return "", false
	}
	return secret, true
}

// DeleteAuthHookSecret serves DELETE on the same path: every hook goes off
// first, so auth never calls a hook it can no longer sign for.
func (h *ProvisioningHandler) DeleteAuthHookSecret(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authHooksSecretProject(w, r)
	if !ok {
		return
	}
	release, ok := h.lockAuthHookSecret(r.Context(), w, projectID)
	if !ok {
		return
	}
	defer release()
	if err := h.authOAuth.Hooks.ClearAuthHookSecret(r.Context(), projectID); err != nil {
		log.Printf("ERROR: switch off the hooks of project %s: %v", projectID, err)
		httpError(w, errAuthHooksSave, http.StatusInternalServerError)
		return
	}
	if err := h.authOAuth.Secrets.Delete(domain.AuthHooksSecretPath(projectID)); err != nil {
		log.Printf("ERROR: delete the hook secret of project %s: %v", projectID, err)
		httpError(w, "failed to delete the secret", http.StatusInternalServerError)
		return
	}
	h.writeAuthAudit(r, projectID, auditActionAuthHookSecretDelete, auditResourceAuthHook, projectID, `{"secretSet":false,"hooksEnabled":false}`)
	h.writeAuthHooks(r.Context(), w, projectID)
}

// lockAuthHookSecret takes the project's secret lock, so a rotation and a
// removal never interleave around the vault call.
func (h *ProvisioningHandler) lockAuthHookSecret(ctx context.Context, w http.ResponseWriter, projectID string) (func(), bool) {
	release, err := h.authOAuth.Hooks.LockAuthHookSecret(ctx, projectID)
	if err != nil {
		log.Printf("ERROR: lock the hook secret of project %s: %v", projectID, err)
		httpError(w, errAuthHooksSave, http.StatusInternalServerError)
		return nil, false
	}
	return release, true
}

func (h *ProvisioningHandler) writeAuthHooks(ctx context.Context, w http.ResponseWriter, projectID string) {
	stored, secretSet, err := h.storedAuthHooks(ctx, projectID)
	if err != nil {
		log.Printf("ERROR: read the auth hooks of project %s: %v", projectID, err)
		httpError(w, errAuthHooksRead, http.StatusInternalServerError)
		return
	}
	response := authHooksResponse{SecretSet: secretSet, Hooks: make([]authHookView, 0, len(stored))}
	for _, hook := range stored {
		response.Hooks = append(response.Hooks, authHookView{Hook: hook.Hook, Enabled: hook.Enabled, URL: hook.URL})
	}
	writeJSON(w, response)
}

// storedAuthHooks is every hook in order and whether the secret is set.
func (h *ProvisioningHandler) storedAuthHooks(ctx context.Context, projectID string) ([]domain.ProjectAuthHook, bool, error) {
	stored, err := h.authOAuth.Hooks.ListAuthHooks(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	secretSet, err := h.authOAuth.Hooks.AuthHookSecretSet(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	return domain.AllAuthHooks(stored), secretSet, nil
}

// authHooksProject validates the project and the wiring; the route's flag
// gate answers 404 while authhooks is off, checked here again to fail closed.
func (h *ProvisioningHandler) authHooksProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	if h.authOAuth.Hooks == nil {
		httpError(w, "auth hooks are not available", http.StatusServiceUnavailable)
		return "", false
	}
	if !h.authHooksOn(r.Context()) {
		http.NotFound(w, r)
		return "", false
	}
	return projectID, true
}

func (h *ProvisioningHandler) authHooksSecretProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID, ok := h.authHooksProject(w, r)
	if !ok {
		return "", false
	}
	if h.authOAuth.Secrets == nil {
		httpError(w, "the vault is not available", http.StatusServiceUnavailable)
		return "", false
	}
	return projectID, true
}

// authHooksForInfo is /info's authHooks: nil while the flag is off or for any
// caller but a platform service (a URL may carry the receiver's token), else
// the URL of each enabled hook. A hook is never reported without its secret.
func (h *ProvisioningHandler) authHooksForInfo(ctx context.Context, projectID string) (*domain.AuthHooksInfo, error) {
	if !h.authHooksOn(ctx) || !auth.TokenGrants(auth.GetToken(ctx), auth.ProjectInfoReadCapability()) {
		return nil, nil
	}
	info := &domain.AuthHooksInfo{}
	if h.authOAuth.Hooks == nil {
		return info, nil
	}
	stored, secretSet, err := h.storedAuthHooks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if !secretSet {
		return info, nil
	}
	for _, hook := range stored {
		if hook.Enabled {
			info.Set(hook.Hook, hook.URL)
		}
	}
	return info, nil
}
