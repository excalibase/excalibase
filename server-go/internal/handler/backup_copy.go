package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// auditDatabaseCopy is the audit action of a started copy (EXC-667).
const auditDatabaseCopy = "database.copy"

// copiesUnavailable answers a platform wired without copies.
const copiesUnavailable = "copies are not available on this platform"

// copyFailed is all a caller learns of a failure that is the platform's own.
const copyFailed = "the copy could not be started; try again, and if it fails again ask your platform administrator to check the provisioning log"

// SetAudit wires the audit log a started copy is recorded in; nil records nothing.
func (h *BackupHandler) SetAudit(audit auditWriter) { h.audit = audit }

// Copy files a copy of the project's database into a new project in its
// organisation, at now, the latest backup or a time (EXC-667). It answers
// with the restore job, polled on GET /restore/{jobId}.
func (h *BackupHandler) Copy(w http.ResponseWriter, r *http.Request) {
	var body domain.CopyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if errors.Is(err, domain.ErrTargetTimeFormat) {
			httpError(w, domain.ErrTargetTimeFormat.Error(), http.StatusBadRequest)
			return
		}
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	req, err := body.RestoreRequest()
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	inst, ok := h.copySource(w, r, req)
	if !ok {
		return
	}
	if h.orchestrator == nil {
		httpError(w, copiesUnavailable, http.StatusServiceUnavailable)
		return
	}
	if err := h.svc.EnsureOrgProjectCapacity(r.Context(), inst); err != nil {
		h.writeCopyCapacityError(w, err)
		return
	}
	targetID, err := h.svc.AllocateProjectID()
	if err != nil {
		httpError(w, "could not allocate a project id for the copy", http.StatusServiceUnavailable)
		return
	}
	req.TargetProjectID = targetID
	job, err := h.orchestrator.Start(r.Context(), inst, req)
	if err != nil {
		h.writeCopyStartError(w, err)
		return
	}
	h.recordCopy(r, job, body)
	writeJSON(w, job)
}

// copySource reads the source project and refuses a copy it cannot give,
// answering the caller itself when it does.
func (h *BackupHandler) copySource(w http.ResponseWriter, r *http.Request, req domain.RestoreRequest) (*domain.DatabaseInstance, bool) {
	inst, err := h.svc.GetInstance(chi.URLParam(r, "projectId"))
	if err != nil {
		log.Printf("copy: read source project: %v", err)
		httpError(w, "could not read the project", http.StatusServiceUnavailable)
		return nil, false
	}
	if inst == nil {
		httpError(w, "project not found", http.StatusNotFound)
		return nil, false
	}
	if inst.DocumentDB && !h.documentDBEnabled {
		httpError(w, service.ErrDocumentDBNotInstalled.Error(), http.StatusConflict)
		return nil, false
	}
	if err := h.svc.CheckCopy(inst, req); err != nil {
		writeCopyRefusal(w, err)
		return nil, false
	}
	if err := h.svc.CheckRestoreTarget(r.Context(), inst, req); err != nil {
		writeRestoreTargetError(w, err)
		return nil, false
	}
	return inst, true
}

func writeCopyRefusal(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrCopyNeedsBackups), errors.Is(err, service.ErrCopyNoBackupStore),
		errors.Is(err, service.ErrCopyNowUnsupported), errors.Is(err, service.ErrCopyNowNeedsRunning):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrUnsupportedBackupMode):
		httpError(w, "this database cannot be copied on this platform", http.StatusConflict)
	default:
		log.Printf("copy: check the source: %v", err)
		httpError(w, copyFailed, http.StatusInternalServerError)
	}
}

// writeCopyCapacityError answers a copy the organisation's plan cannot take.
// A copy is never in place, so no in-place restore is offered.
func (h *BackupHandler) writeCopyCapacityError(w http.ResponseWriter, err error) {
	var limit *service.OrgProjectLimitError
	switch {
	case errors.As(err, &limit):
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": limit.Error(), "code": restoreLimitCode, "status": http.StatusConflict,
		})
	case errors.Is(err, service.ErrRestoreDiskAbovePlan):
		httpError(w, service.ErrRestoreDiskAbovePlan.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrOrgCapacityNotConfigured):
		httpError(w, copiesUnavailable, http.StatusServiceUnavailable)
	case !writeProjectCreationError(w, err):
		log.Printf("copy: admit the new project: %v", err)
		httpError(w, copyFailed, http.StatusInternalServerError)
	}
}

// writeCopyStartError answers a copy job the orchestrator refused to start.
func (h *BackupHandler) writeCopyStartError(w http.ResponseWriter, err error) {
	if errors.Is(err, storage.ErrRestoreAlreadyRunning) {
		httpError(w, err.Error(), http.StatusConflict)
		return
	}
	h.writeCopyCapacityError(w, err)
}

// recordCopy audits a started copy by ids and point only: nothing in it
// opens either database.
func (h *BackupHandler) recordCopy(r *http.Request, job *domain.RestoreJob, body domain.CopyRequest) {
	if h.audit == nil {
		return
	}
	point := body.Point
	if point == "" {
		point = domain.CopyPointNow
	}
	details := map[string]any{"jobId": job.ID, "copyProjectId": job.NewProjectID, "point": point}
	if body.TargetTime != nil {
		details["targetTime"] = body.TargetTime.UTC().Format(time.RFC3339)
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		// The error can quote the caller's input, so only the fact is logged.
		log.Printf("ERROR: audit %s: the details could not be encoded", auditDatabaseCopy)
		return
	}
	now := time.Now()
	entry := &domain.AuditEntry{UserID: actorID(r), Action: auditDatabaseCopy, Resource: "project", ResourceID: job.NewProjectID,
		ProjectID: job.SourceProjectID, Details: string(encoded), IPAddress: clientIP(r), Timestamp: &now}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s of %s: %v", auditDatabaseCopy, safeLog(job.SourceProjectID), err)
	}
}
