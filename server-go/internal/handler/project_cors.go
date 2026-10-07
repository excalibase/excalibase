package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// errCorsNotConfigured is returned when no ProjectCorsStore is wired — the
// allowlist API fails closed rather than pretending a setting was saved.
const errCorsNotConfigured = "cors allowlist not configured"

// corsResponse is the wire shape of GET/PUT /api/projects/{id}/cors.
//
//	allowedOrigins — the project's canonical allowlist (what PUT writes)
//	allowWildcard  — true when the list is the single "*" entry
type corsResponse struct {
	AllowedOrigins []string `json:"allowedOrigins"`
	AllowWildcard  bool     `json:"allowWildcard"`
}

// corsRequest is the PUT body. allowWildcard must be set to write "*"; it is
// an explicit confirmation, not a stored setting.
type corsRequest struct {
	AllowedOrigins []string `json:"allowedOrigins"`
	AllowWildcard  bool     `json:"allowWildcard"`
}

// SetCorsStore wires the per-project browser-origin allowlist store (EXC-23).
func (h *ProvisioningHandler) SetCorsStore(s storage.ProjectCorsStore) {
	h.corsStore = s
}

// GetCors serves GET /api/projects/{projectId}/cors.
func (h *ProvisioningHandler) GetCors(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.corsRequestProject(w, r)
	if !ok {
		return
	}
	origins, err := h.corsStore.GetCorsOrigins(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, corsResponseFor(origins))
}

// PutCors serves PUT /api/projects/{projectId}/cors with body
// {"allowedOrigins": [...], "allowWildcard": false}. The list replaces the
// project's setting; it is validated before anything is written. The data
// plane picks it up on its next /info refresh (TTL, not push).
func (h *ProvisioningHandler) PutCors(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.corsRequestProject(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var body corsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	origins, err := domain.ParseCorsOrigins(body.AllowedOrigins, body.AllowWildcard)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if err := h.corsStore.SetCorsOrigins(r.Context(), projectID, origins); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, corsResponseFor(origins))
}

// corsOriginRequest is the POST .../cors/origins body. appId, when set, marks
// the origin as the app's own, so deleting the app removes it.
type corsOriginRequest struct {
	Origin string `json:"origin"`
	AppID  string `json:"appId"`
}

type corsOriginResponse struct {
	corsResponse
	Added   bool `json:"added"`
	Removed bool `json:"removed"`
}

// AddCorsOrigin serves POST /api/projects/{projectId}/cors/origins: one
// origin is added under the row's lock, so a concurrent edit is never lost.
func (h *ProvisioningHandler) AddCorsOrigin(w http.ResponseWriter, r *http.Request) {
	projectID, editor, ok := h.corsEditorFor(w, r)
	if !ok {
		return
	}
	var body corsOriginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*1024)).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	if body.AppID != "" && !isValidID(body.AppID) {
		httpError(w, "invalid appId", http.StatusBadRequest)
		return
	}
	origin, ok := singleCorsOrigin(w, body.Origin)
	if !ok {
		return
	}
	added, origins, err := editor.AddCorsOrigin(r.Context(), projectID, origin, body.AppID)
	if errors.Is(err, domain.ErrInvalidCorsOrigin) {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, corsOriginResponse{corsResponse: corsResponseFor(origins), Added: added})
}

// RemoveCorsOrigin serves DELETE /api/projects/{projectId}/cors/origins?origin=...
func (h *ProvisioningHandler) RemoveCorsOrigin(w http.ResponseWriter, r *http.Request) {
	projectID, editor, ok := h.corsEditorFor(w, r)
	if !ok {
		return
	}
	origin, ok := singleCorsOrigin(w, r.URL.Query().Get("origin"))
	if !ok {
		return
	}
	removed, origins, err := editor.RemoveCorsOrigin(r.Context(), projectID, origin)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, corsOriginResponse{corsResponse: corsResponseFor(origins), Removed: removed})
}

func (h *ProvisioningHandler) corsEditorFor(w http.ResponseWriter, r *http.Request) (string, storage.ProjectCorsEditor, bool) {
	projectID, ok := h.corsRequestProject(w, r)
	if !ok {
		return "", nil, false
	}
	editor, ok := h.corsStore.(storage.ProjectCorsEditor)
	if !ok {
		httpError(w, errCorsNotConfigured, http.StatusServiceUnavailable)
		return "", nil, false
	}
	return projectID, editor, true
}

// singleCorsOrigin is one canonical origin; the wildcard is set only through
// PUT, where it must be confirmed.
func singleCorsOrigin(w http.ResponseWriter, raw string) (string, bool) {
	parsed, err := domain.ParseCorsOrigins([]string{raw}, false)
	if err != nil || len(parsed) != 1 {
		httpError(w, "origin must be one origin, e.g. http://localhost:5173", http.StatusBadRequest)
		return "", false
	}
	return parsed[0], true
}

// corsRequestProject validates the path project and the store wiring shared
// by both verbs, writing the error response itself when either fails.
func (h *ProvisioningHandler) corsRequestProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	if h.corsStore == nil {
		httpError(w, errCorsNotConfigured, http.StatusServiceUnavailable)
		return "", false
	}
	return projectID, true
}

// corsOriginsForInfo is the list exposed on /info. A missing store reads as
// "no origins" (the data plane then sends no CORS headers); a store failure is
// surfaced so the caller keeps its last good copy instead of caching a deny.
func (h *ProvisioningHandler) corsOriginsForInfo(ctx context.Context, projectID string) ([]string, error) {
	if h.corsStore == nil {
		return []string{}, nil
	}
	return h.corsStore.GetCorsOrigins(ctx, projectID)
}

func corsResponseFor(origins []string) corsResponse {
	if origins == nil {
		origins = []string{}
	}
	return corsResponse{
		AllowedOrigins: origins,
		AllowWildcard:  domain.IsCorsWildcard(origins),
	}
}
