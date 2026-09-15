package handler

import (
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type AlertHandler struct{ svc *service.AlertingService }

func NewAlertHandler(svc *service.AlertingService) *AlertHandler { return &AlertHandler{svc: svc} }

// Routes registers the platform-wide alert reads. The per-project read is
// registered by ProjectRoutes under a {projectId} mount so the project gate
// runs with the param bound (a gate on this level would see no projectId).
func (h *AlertHandler) Routes(r chi.Router) {
	r.Get("/", h.GetActive)
	r.Get("/history", h.GetHistory)
}

// ProjectRoutes registers the alert reads relative to an already-bound
// {projectId}; the mount is responsible for RequireProjectAccess.
func (h *AlertHandler) ProjectRoutes(r chi.Router) {
	r.Get("/", h.GetForProject)
}

func (h *AlertHandler) GetActive(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, h.svc.GetActiveAlerts())
}

func (h *AlertHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil {
			limit = v
		}
	}
	writeJSON(w, h.svc.GetAlertHistory(limit))
}

func (h *AlertHandler) GetForProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	writeJSON(w, h.svc.GetActiveAlertsForProject(projectID))
}
