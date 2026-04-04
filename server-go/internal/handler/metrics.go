package handler

import (
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type MetricsHandler struct {
	svc *service.MetricsService
}

func NewMetricsHandler(svc *service.MetricsService) *MetricsHandler {
	return &MetricsHandler{svc: svc}
}

func (h *MetricsHandler) Routes(r chi.Router) {
	r.Get("/current", h.GetCurrent)
	r.Get("/history", h.GetHistory)
}

func (h *MetricsHandler) GetCurrent(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	metrics, err := h.svc.GetCurrentMetrics(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, metrics)
}

func (h *MetricsHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil {
			limit = parsed
		}
	}
	history, err := h.svc.GetMetricsHistory(r.Context(), projectID, limit)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, history)
}
