package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// errAuthSettingsNotConfigured is returned when no ProjectAuthSettingsStore
// is wired — the API fails closed rather than pretending a setting was saved.
const errAuthSettingsNotConfigured = "project auth settings not configured"

// authSettingsResponse is the wire shape of GET/PUT
// /api/projects/{id}/auth-settings.
type authSettingsResponse struct {
	RequireEmailVerification bool   `json:"requireEmailVerification"`
	SiteURL                  string `json:"siteUrl"`
}

// authSettingsRequest is the PUT body.
type authSettingsRequest struct {
	RequireEmailVerification bool   `json:"requireEmailVerification"`
	SiteURL                  string `json:"siteUrl"`
}

// GetAuthSettings serves GET /api/projects/{projectId}/auth-settings.
func (h *ProvisioningHandler) GetAuthSettings(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authSettingsRequestProject(w, r)
	if !ok {
		return
	}
	settings, _, err := h.authSettingsStore.GetAuthSettings(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, authSettingsResponseFor(settings))
}

// PutAuthSettings serves PUT /api/projects/{projectId}/auth-settings with
// body {"requireEmailVerification": bool, "siteUrl": string}. The site URL is
// validated and canonicalised before anything is written. The auth service
// picks up the change on its next /info refresh (TTL, not push).
func (h *ProvisioningHandler) PutAuthSettings(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authSettingsRequestProject(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var body authSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	siteURL, err := domain.ValidateSiteURL(body.SiteURL)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	settings := domain.ProjectAuthSettings{
		RequireEmailVerification: body.RequireEmailVerification,
		SiteURL:                  siteURL,
	}
	if err := h.authSettingsStore.SetAuthSettings(r.Context(), projectID, settings); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, authSettingsResponseFor(settings))
}

// authSettingsRequestProject validates the path project and the store wiring
// shared by both verbs, writing the error response itself when either fails.
func (h *ProvisioningHandler) authSettingsRequestProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	if h.authSettingsStore == nil {
		httpError(w, errAuthSettingsNotConfigured, http.StatusServiceUnavailable)
		return "", false
	}
	return projectID, true
}

// authSettingsForInfo is the settings exposed on /info. A missing store reads
// as the zero value (verification off, no site URL); a store failure is
// surfaced so the caller keeps its last good copy instead of caching a wrong
// default.
func (h *ProvisioningHandler) authSettingsForInfo(ctx context.Context, projectID string) (domain.ProjectAuthSettings, error) {
	if h.authSettingsStore == nil {
		return domain.ProjectAuthSettings{}, nil
	}
	settings, _, err := h.authSettingsStore.GetAuthSettings(ctx, projectID)
	return settings, err
}

func authSettingsResponseFor(settings domain.ProjectAuthSettings) authSettingsResponse {
	return authSettingsResponse{
		RequireEmailVerification: settings.RequireEmailVerification,
		SiteURL:                  settings.SiteURL,
	}
}
