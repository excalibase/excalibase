package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

// maxRegistryCredentialBodyBytes fits the longest username and token with their JSON framing.
const maxRegistryCredentialBodyBytes = 2 * (apphost.MaxRegistryUsernameLength + apphost.MaxRegistryPasswordLength)

type RegistryCredentials interface {
	Set(projectID, registry string, cred apphost.RegistryCredential) (string, error)
	List(projectID string) ([]string, error)
	Remove(ctx context.Context, projectID, registry string) error
}

// RegistryCredentialHandler stores a project's private-registry credentials.
// Write-only: no response carries a username or a password back.
type RegistryCredentialHandler struct {
	creds RegistryCredentials
}

func NewRegistryCredentialHandler(creds RegistryCredentials) *RegistryCredentialHandler {
	return &RegistryCredentialHandler{creds: creds}
}

func (h *RegistryCredentialHandler) available(w http.ResponseWriter) bool {
	if h.creds == nil {
		httpError(w, "registry credentials are unavailable: no vault is configured", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h *RegistryCredentialHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Put("/{registry}", h.Set)
	r.Delete("/{registry}", h.Remove)
}

type registryEntry struct {
	Registry string `json:"registry"`
}

func (h *RegistryCredentialHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok || !h.available(w) {
		return
	}
	registries, err := h.creds.List(projectID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	out := make([]registryEntry, 0, len(registries))
	for _, registry := range registries {
		out = append(out, registryEntry{Registry: registry})
	}
	writeJSON(w, out)
}

func (h *RegistryCredentialHandler) Set(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok || !h.available(w) {
		return
	}
	var cred apphost.RegistryCredential
	r.Body = http.MaxBytesReader(w, r.Body, maxRegistryCredentialBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&cred); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	registry, err := h.creds.Set(projectID, chi.URLParam(r, "registry"), cred)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, map[string]any{"registry": registry, "set": true})
}

func (h *RegistryCredentialHandler) Remove(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok || !h.available(w) {
		return
	}
	if err := h.creds.Remove(r.Context(), projectID, chi.URLParam(r, "registry")); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeError never echoes the request: a refusal names what was wrong, not the value sent.
func (h *RegistryCredentialHandler) writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, service.ErrInvalidRegistryCredential) {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	httpError(w, safeError(err), http.StatusInternalServerError)
}
