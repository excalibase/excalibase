package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type BackupHandler struct {
	svc *service.BackupService
}

func NewBackupHandler(svc *service.BackupService) *BackupHandler {
	return &BackupHandler{svc: svc}
}

// Service exposes the underlying BackupService so the platform can
// share it across HTTP handlers and the scheduler.
func (h *BackupHandler) Service() *service.BackupService { return h.svc }

func (h *BackupHandler) Routes(r chi.Router) {
	r.Post("/trigger", h.TriggerBackup)
	r.Get("/list", h.ListBackups)
	r.Post("/restore", h.Restore)
}

func (h *BackupHandler) TriggerBackup(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	result, err := h.svc.TriggerManualBackup(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (h *BackupHandler) ListBackups(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	backups, err := h.svc.ListBackups(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	// Frontend expects { backups: [], backupEnabled, schedule, retentionDays }
	inst, _ := h.svc.GetInstance(projectID)
	backupEnabled := false
	schedule := ""
	retentionDays := 0
	if inst != nil {
		if inst.BackupEnabled != nil {
			backupEnabled = *inst.BackupEnabled
		}
		schedule = inst.BackupSchedule
		if inst.BackupRetentionDays != nil {
			retentionDays = *inst.BackupRetentionDays
		}
	}
	writeJSON(w, map[string]interface{}{
		"backups":       backups,
		"backupEnabled": backupEnabled,
		"schedule":      schedule,
		"retentionDays": retentionDays,
	})
}

func (h *BackupHandler) Restore(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var req domain.RestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	resp, err := h.svc.RestoreFromBackup(r.Context(), projectID, req)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}
