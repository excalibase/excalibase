package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// AddDatabase gives a project created without a database its database
// (EXC-426). The body carries only database settings — engine, major,
// DocumentDB, parameters, storage class — and the project's plan sizes it.
// The answer is the project as the add left it: with its database, or still
// without one and the failure named, so the add can be retried.
func (h *ProvisioningHandler) AddDatabase(w http.ResponseWriter, r *http.Request) {
	var req domain.ProvisioningRequest
	if !decodeStrict(w, r, &req) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	resp, err := h.svc.AddDatabase(r.Context(), projectID, req)
	if err != nil {
		writeAddDatabaseError(w, projectID, err)
		return
	}
	writeJSON(w, resp)
}

func writeAddDatabaseError(w http.ResponseWriter, projectID string, err error) {
	switch {
	case errors.Is(err, storage.ErrProjectHasDatabase):
		httpError(w, storage.ErrProjectHasDatabase.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrAddDatabaseRequest), errors.Is(err, service.ErrNoDatabaseNeedsKubernetes):
		httpError(w, safeError(err), http.StatusBadRequest)
	case errors.Is(err, service.ErrProjectNotFound):
		httpError(w, "project not found", http.StatusNotFound)
	case unsettledOr(err, 0) != 0:
		httpError(w, safeError(err), http.StatusConflict)
	case writeProjectCreationError(w, err):
	default:
		// Everything else is a refusal of the settings asked for: an unknown
		// major, DocumentDB on a major without it, parameters beyond the plan.
		log.Printf("add database to %s refused: %v", projectID, err)
		httpError(w, safeError(err), http.StatusBadRequest)
	}
}
