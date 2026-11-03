package handler

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/advisors"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/go-chi/chi/v5"
)

// AdvisorsRunner checks one project (advisors.Service).
type AdvisorsRunner interface {
	Run(ctx context.Context, projectID string) (*advisors.Report, error)
}

// AdvisorsHandler serves a project's security and performance checks (EXC-655).
type AdvisorsHandler struct {
	runner AdvisorsRunner
}

func NewAdvisorsHandler(runner AdvisorsRunner) *AdvisorsHandler {
	return &AdvisorsHandler{runner: runner}
}

func (h *AdvisorsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.runner == nil {
		httpError(w, "advisors are not available on this platform", http.StatusServiceUnavailable)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	report, err := h.runner.Run(r.Context(), projectID)
	switch {
	case err == nil:
		writeJSON(w, report)
	case errors.Is(err, projectdb.ErrNotServable):
		httpError(w, "project is not running", http.StatusConflict)
	case errors.Is(err, domain.ErrNoDatabase):
		httpError(w, domain.ErrNoDatabase.Error(), http.StatusConflict)
	default:
		log.Printf("advisors %s: %v", projectID, err)
		httpError(w, "the advisors could not read the project's database", http.StatusInternalServerError)
	}
}
