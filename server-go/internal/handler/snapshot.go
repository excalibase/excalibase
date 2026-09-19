package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/security"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

// errSnapshotNotFound is the single answer for "no such snapshot" and "that
// snapshot belongs to another project" — confirming existence would defeat
// the ownership check.
const errSnapshotNotFound = "snapshot not found"

type SnapshotHandler struct{ svc *service.SnapshotService }

func NewSnapshotHandler(svc *service.SnapshotService) *SnapshotHandler { return &SnapshotHandler{svc: svc} }

func (h *SnapshotHandler) Routes(r chi.Router) {
	r.Post("/export", h.Export)
	r.Get("/", h.List)
	r.Get("/{snapshotId}/download", h.Download)
	r.Delete("/{snapshotId}", h.Delete)
}

func (h *SnapshotHandler) Export(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var req domain.SnapshotExportRequest
	json.NewDecoder(r.Body).Decode(&req)
	info, err := h.svc.ExportSnapshot(r.Context(), projectID, req)
	if err != nil { httpError(w, safeError(err), http.StatusInternalServerError); return }
	writeJSON(w, info)
}

func (h *SnapshotHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	list, _ := h.svc.ListSnapshots(projectID)
	writeJSON(w, list)
}

// snapshotIDParam validates the {snapshotId} path parameter at the handler
// boundary. A malformed id is a bad request and never reaches the store.
func snapshotIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	snapshotID := chi.URLParam(r, "snapshotId")
	if err := security.ValidateIdentifier(snapshotID); err != nil {
		httpError(w, "invalid snapshot id", http.StatusBadRequest)
		return "", false
	}
	return snapshotID, true
}

func (h *SnapshotHandler) Download(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	snapshotID, ok := snapshotIDParam(w, r)
	if !ok {
		return
	}
	data, filename, err := h.svc.DownloadSnapshot(projectID, snapshotID)
	if err != nil {
		httpError(w, errSnapshotNotFound, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(data)
}

func (h *SnapshotHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	snapshotID, ok := snapshotIDParam(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteSnapshot(projectID, snapshotID); err != nil {
		if errors.Is(err, service.ErrSnapshotNotFound) {
			httpError(w, errSnapshotNotFound, http.StatusNotFound)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}
