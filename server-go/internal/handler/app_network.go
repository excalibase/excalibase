package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// AppNetworkAPI is the slice of the private-network service this handler uses.
type AppNetworkAPI interface {
	Describe(ctx context.Context, projectID string) (service.AppNetworkView, error)
	Set(ctx context.Context, projectID string, enabled bool) (service.AppNetworkView, error)
}

// AppNetworkHandler serves /api/projects/{projectId}/app-network (EXC-524):
// whether the project's apps may reach each other by name. Off by default.
type AppNetworkHandler struct {
	api AppNetworkAPI
}

func NewAppNetworkHandler(api AppNetworkAPI) *AppNetworkHandler {
	return &AppNetworkHandler{api: api}
}

func (h *AppNetworkHandler) Routes(r chi.Router) {
	r.Get("/", h.Get)
	r.Put("/", h.Put)
}

// appNetworkResponse: applied is whether the cluster holds the policy now;
// canChange mirrors the route's Admin write policy for display only.
type appNetworkResponse struct {
	ProjectID      string `json:"projectId"`
	PrivateNetwork bool   `json:"privateNetwork"`
	Applied        bool   `json:"applied"`
	CanChange      bool   `json:"canChange"`
}

// appNetworkRequest's field is a pointer so a body that says nothing is refused, not read as off.
type appNetworkRequest struct {
	PrivateNetwork *bool `json:"privateNetwork"`
}

func (h *AppNetworkHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID, ok := appNetworkProject(w, r)
	if !ok {
		return
	}
	view, err := h.api.Describe(r.Context(), projectID)
	if err != nil {
		writeAppNetworkError(w, err)
		return
	}
	writeJSON(w, appNetworkResponseFor(r, view))
}

func (h *AppNetworkHandler) Put(w http.ResponseWriter, r *http.Request) {
	projectID, ok := appNetworkProject(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body appNetworkRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.PrivateNetwork == nil {
		httpError(w, "body must be {\"privateNetwork\": true|false}", http.StatusBadRequest)
		return
	}
	view, err := h.api.Set(r.Context(), projectID, *body.PrivateNetwork)
	if err != nil {
		writeAppNetworkError(w, err)
		return
	}
	writeJSON(w, appNetworkResponseFor(r, view))
}

func appNetworkProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	return projectID, true
}

func appNetworkResponseFor(r *http.Request, view service.AppNetworkView) appNetworkResponse {
	access := custommw.ProjectAccessFromContext(r.Context())
	return appNetworkResponse{
		ProjectID:      view.ProjectID,
		PrivateNetwork: view.PrivateNetwork,
		Applied:        view.Applied,
		CanChange:      access != nil && access.RoleAtLeast(domain.OrgRoleAdmin),
	}
}

func writeAppNetworkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrAppNetworkProjectNotFound):
		httpError(w, "project not found", http.StatusNotFound)
	case errors.Is(err, service.ErrAppNetworkUnsupported),
		errors.Is(err, service.ErrProjectNotActive),
		errors.Is(err, service.ErrProjectOperationRunning):
		httpError(w, safeError(err), http.StatusConflict)
	default:
		log.Printf("app private network: %v", err)
		httpError(w, "the cluster did not confirm the change; read the setting again", http.StatusBadGateway)
	}
}
