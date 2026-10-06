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
// without one and the failure named, so the add can be retried. A caller
// that prefers not to wait (RFC 7240, Studio) is answered 202 once the
// project is PROVISIONING and follows the build through GET /{projectId}.
func (h *ProvisioningHandler) AddDatabase(w http.ResponseWriter, r *http.Request) {
	var req domain.ProvisioningRequest
	statedTier, ok := decodeProvisioningRequest(w, r, &req)
	if !ok {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if statedTier != nil {
		inst, err := h.svc.GetInstance(projectID)
		if err != nil {
			httpError(w, "project not found", http.StatusNotFound)
			return
		}
		if !h.confirmStatedTier(w, r, inst.OrgID, statedTier) {
			return
		}
	}
	if prefersRespondAsync(r) {
		resp, err := h.svc.AddDatabaseInBackground(r.Context(), projectID, req)
		if err != nil {
			writeAddDatabaseError(w, projectID, err)
			return
		}
		writeJSONStatus(w, http.StatusAccepted, resp)
		return
	}
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
	case errors.Is(err, service.ErrDocumentDBNotInstalled):
		httpError(w, service.ErrDocumentDBNotInstalled.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrAddDatabaseRequest), errors.Is(err, service.ErrNoDatabaseNeedsKubernetes),
		errors.Is(err, service.ErrDatabaseRequestInvalid):
		httpError(w, safeError(err), http.StatusBadRequest)
	case errors.Is(err, service.ErrProjectNotFound):
		httpError(w, "project not found", http.StatusNotFound)
	case unsettledOr(err, 0) != 0, errors.Is(err, storage.ErrProjectDeleting):
		httpError(w, safeError(err), http.StatusConflict)
	case writeProjectCreationError(w, err):
	default:
		log.Printf("ERROR: add database to %s: %v", projectID, err)
		httpError(w, "the database could not be added", http.StatusInternalServerError)
	}
}
