package handler

import (
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type PerformanceHandler struct {
	svc *service.PerformanceService
}

func NewPerformanceHandler(svc *service.PerformanceService) *PerformanceHandler {
	return &PerformanceHandler{svc: svc}
}

func (h *PerformanceHandler) Routes(r chi.Router) {
	r.Get("/summary", h.GetSummary)
	r.Get("/top-queries", h.GetTopQueries)
	r.Get("/wait-events", h.GetWaitEvents)
}

func (h *PerformanceHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	summary, err := h.svc.GetSummary(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, summary)
}

func (h *PerformanceHandler) GetTopQueries(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	limit := 10
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil {
			limit = v
		}
	}
	queries, err := h.svc.GetTopQueries(r.Context(), projectID, limit)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, queries)
}

func (h *PerformanceHandler) GetWaitEvents(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	events, err := h.svc.GetWaitEvents(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, events)
}
