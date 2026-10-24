package handler

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// RequestCharter answers a project's request history.
type RequestCharter interface {
	Chart(ctx context.Context, projectID, rangeName string) (*domain.RequestCharts, error)
}

// RequestMetricsHandler serves Studio's request charts (EXC-656).
type RequestMetricsHandler struct {
	source RequestCharter
}

func NewRequestMetricsHandler(source RequestCharter) *RequestMetricsHandler {
	return &RequestMetricsHandler{source: source}
}

// Charts answers ?range=1h|24h|7d, the last hour by default.
func (h *RequestMetricsHandler) Charts(w http.ResponseWriter, r *http.Request) {
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "1h"
	}
	projectID := chi.URLParam(r, "projectId")
	charts, err := h.source.Chart(r.Context(), projectID, rangeName)
	if errors.Is(err, service.ErrUnknownChartRange) {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("request charts of %s: %v", safeLog(projectID), err)
		httpError(w, "request metrics could not be read", http.StatusInternalServerError)
		return
	}
	writeJSON(w, charts)
}
