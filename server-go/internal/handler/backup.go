package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type BackupHandler struct {
	svc        *service.BackupService
	scheduler  *service.BackupScheduler     // optional; nil disables /schedule routes
	orchestrator *service.RestoreOrchestrator // optional; nil → restore is synchronous
}

func NewBackupHandler(svc *service.BackupService) *BackupHandler {
	return &BackupHandler{svc: svc}
}

// SetScheduler wires the scheduler so the /schedule routes can write
// to the persistent store + the running cron. Wired post-construction
// because the scheduler isn't available at handler init time.
func (h *BackupHandler) SetScheduler(s *service.BackupScheduler) { h.scheduler = s }

// SetRestoreOrchestrator wires the async restore pipeline. When set,
// /restore returns RUNNING immediately + a job id; callers poll via
// GET /restore/{jobId}. When nil, /restore behaves as it did pre-Phase
// 3 — synchronous, returns the response shape from BackupAdapter.Restore.
func (h *BackupHandler) SetRestoreOrchestrator(o *service.RestoreOrchestrator) { h.orchestrator = o }

// Service exposes the underlying BackupService so the platform can
// share it across HTTP handlers and the scheduler.
func (h *BackupHandler) Service() *service.BackupService { return h.svc }

func (h *BackupHandler) Routes(r chi.Router) {
	r.Post("/trigger", h.TriggerBackup)
	r.Get("/list", h.ListBackups)
	r.Post("/restore", h.Restore)
	r.Get("/restore/{jobId}", h.GetRestoreJob)
	r.Get("/wal-lag", h.GetWalLag)
	r.Post("/schedule", h.UpsertSchedule)
	r.Delete("/schedule", h.DeleteSchedule)
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
	if h.orchestrator != nil {
		// Async path: orchestrator returns RUNNING immediately. The
		// caller polls /restore/{jobId} for status.
		inst, err := h.svc.GetInstance(projectID)
		if err != nil || inst == nil {
			httpError(w, "project not found", http.StatusNotFound)
			return
		}
		job, err := h.orchestrator.Start(r.Context(), inst, req)
		if err != nil {
			httpError(w, safeError(err), http.StatusBadRequest)
			return
		}
		writeJSON(w, job)
		return
	}
	resp, err := h.svc.RestoreFromBackup(r.Context(), projectID, req)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, resp)
}

// GetRestoreJob polls a restore job by id, scoped to the project in the URL.
// 404 both when the job doesn't exist and when it belongs to another project
// — confirming existence would be the disclosure. The orchestrator updates
// the row as it progresses.
func (h *BackupHandler) GetRestoreJob(w http.ResponseWriter, r *http.Request) {
	if h.orchestrator == nil {
		httpError(w, "restore orchestrator not configured", http.StatusServiceUnavailable)
		return
	}
	projectID := chi.URLParam(r, "projectId")
	jobID := chi.URLParam(r, "jobId")
	job, err := h.orchestrator.Get(r.Context(), projectID, jobID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if job == nil {
		httpError(w, "restore job not found", http.StatusNotFound)
		return
	}
	writeJSON(w, job)
}

// GetWalLag is a Docker-mode-only endpoint. K8s adapter doesn't
// implement WalLagAdvertiser so the dispatch returns 501.
func (h *BackupHandler) GetWalLag(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	info, err := h.svc.GetWalLag(r.Context(), projectID)
	if err != nil {
		if errors.Is(err, service.ErrWalLagUnsupported) {
			httpError(w, "wal-lag not implemented for this deployment mode", http.StatusNotImplemented)
			return
		}
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, info)
}

// UpsertSchedule writes a backup_schedules row + replays it into
// the running cron. Body:
//
//   { "cron": "0 2 * * *", "retentionDays": 7, "enabled": true }
func (h *BackupHandler) UpsertSchedule(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var body struct {
		Cron          string `json:"cron"`
		RetentionDays int    `json:"retentionDays"`
		Enabled       bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	if body.Cron == "" {
		httpError(w, "cron is required", http.StatusBadRequest)
		return
	}
	if h.scheduler == nil {
		httpError(w, "scheduler not configured", http.StatusServiceUnavailable)
		return
	}
	sch := &domain.BackupSchedule{
		ProjectID:     projectID,
		Cron:          body.Cron,
		RetentionDays: body.RetentionDays,
		Enabled:       body.Enabled,
	}
	if err := h.scheduler.Register(r.Context(), sch); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	writeJSON(w, sch)
}

// DeleteSchedule removes the persistent schedule + the live cron job.
func (h *BackupHandler) DeleteSchedule(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if h.scheduler == nil {
		httpError(w, "scheduler not configured", http.StatusServiceUnavailable)
		return
	}
	if err := h.scheduler.Unregister(r.Context(), projectID); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
