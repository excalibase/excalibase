package handler

import (
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type AlertHandler struct{ svc *service.AlertingService }

func NewAlertHandler(svc *service.AlertingService) *AlertHandler { return &AlertHandler{svc: svc} }

func (h *AlertHandler) Routes(r chi.Router) {
	r.Get("/", h.GetActive)
	r.Get("/history", h.GetHistory)
	r.Get("/project/{projectId}", h.GetForProject)
}

func (h *AlertHandler) GetActive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.svc.GetActiveAlerts())
}

func (h *AlertHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil { limit = v }
	}
	writeJSON(w, h.svc.GetAlertHistory(limit))
}

func (h *AlertHandler) GetForProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	writeJSON(w, h.svc.GetActiveAlertsForProject(projectID))
}
