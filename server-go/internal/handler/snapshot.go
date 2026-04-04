package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

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

func (h *SnapshotHandler) Download(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	snapshotID := chi.URLParam(r, "snapshotId")
	data, filename, err := h.svc.DownloadSnapshot(projectID, snapshotID)
	if err != nil { httpError(w, safeError(err), http.StatusNotFound); return }
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(data)
}

func (h *SnapshotHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	snapshotID := chi.URLParam(r, "snapshotId")
	h.svc.DeleteSnapshot(projectID, snapshotID)
	writeJSON(w, map[string]string{"status": "deleted"})
}
