package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type MigrationHandler struct{ svc *service.MigrationService }

func NewMigrationHandler(svc *service.MigrationService) *MigrationHandler {
	return &MigrationHandler{svc: svc}
}

func (h *MigrationHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Apply)
}

func (h *MigrationHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	list, _ := h.svc.ListMigrations(projectID)
	writeJSON(w, list)
}

func (h *MigrationHandler) Apply(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var req domain.MigrationRequest
	json.NewDecoder(r.Body).Decode(&req)
	rec, err := h.svc.ApplyMigration(r.Context(), projectID, req)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, rec)
}
