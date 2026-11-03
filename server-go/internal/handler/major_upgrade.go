package handler

import (
	"context"
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

// Major version upgrade (EXC-668): any member reads where the project stands,
// an Admin starts a run. The run is asynchronous; Studio polls the status.

// MajorUpgradeRunner is the service the routes call.
type MajorUpgradeRunner interface {
	Status(ctx context.Context, projectID string) (*service.MajorUpgradeStatus, error)
	Start(ctx context.Context, projectID string, req domain.MajorUpgradeRequest, actor string) (*domain.MajorUpgrade, error)
}

type MajorUpgradeHandler struct {
	runs  MajorUpgradeRunner
	audit auditWriter
}

// NewMajorUpgradeHandler takes an optional audit log; nil records nothing.
func NewMajorUpgradeHandler(runs MajorUpgradeRunner, audit auditWriter) *MajorUpgradeHandler {
	return &MajorUpgradeHandler{runs: runs, audit: audit}
}

// Routes mounts GET and POST; the router applies the roles.
func (h *MajorUpgradeHandler) Routes(r chi.Router) {
	r.Get("/", h.Status)
	r.Post("/", h.Start)
}

// Status answers the project's versions, blockers and latest run.
func (h *MajorUpgradeHandler) Status(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	status, err := h.runs.Status(r.Context(), projectID)
	if err != nil {
		writeMajorUpgradeError(w, projectID, err)
		return
	}
	writeJSON(w, status)
}

type majorUpgradeRequest struct {
	TargetVersion string `json:"targetVersion"`
	Confirm       bool   `json:"confirm"`
}

// Start begins a run and answers it as started.
func (h *MajorUpgradeHandler) Start(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var body majorUpgradeRequest
	if !decodeStrict(w, r, &body) {
		return
	}
	run, err := h.runs.Start(r.Context(), projectID, domain.MajorUpgradeRequest{TargetVersion: body.TargetVersion, Confirm: body.Confirm}, actorID(r))
	if err != nil {
		writeMajorUpgradeError(w, projectID, err)
		return
	}
	h.record(r, run)
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"upgrade": run})
}

func (h *MajorUpgradeHandler) record(r *http.Request, run *domain.MajorUpgrade) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]string{"from": run.FromVersion, "to": run.ToVersion})
	now := time.Now()
	entry := &domain.AuditEntry{UserID: actorID(r), Action: "database.major_upgrade.start", Resource: "database",
		ResourceID: run.ID, ProjectID: run.ProjectID, Details: string(details), IPAddress: clientIP(r), Timestamp: &now}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit major upgrade %s: %v", run.ID, err)
	}
}

// writeMajorUpgradeError answers a refusal with its fixed sentence and a
// platform failure with nothing but that it failed.
func writeMajorUpgradeError(w http.ResponseWriter, projectID string, err error) {
	var blocked *service.MajorUpgradeBlockedError
	switch {
	case errors.As(err, &blocked):
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error": service.ErrMajorUpgradeBlocked.Error(), "blockers": blocked.Blockers, "status": http.StatusConflict})
	case errors.Is(err, service.ErrProjectNotFound):
		httpError(w, "project not found", http.StatusNotFound)
	case errors.Is(err, domain.ErrMajorUpgradeUnconfirmed), errors.Is(err, domain.ErrMajorUpgradeTarget),
		errors.Is(err, service.ErrMajorUpgradeTargetNotOffered):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, service.ErrMajorUpgradeUnsupported), errors.Is(err, service.ErrMajorUpgradeNeedsBackups),
		errors.Is(err, service.ErrProjectNotActive), errors.Is(err, storage.ErrMajorUpgradeRunning):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, service.ErrProjectOperationRunning):
		httpError(w, "another operation is running on this project; try again when it ends", http.StatusConflict)
	case errors.Is(err, service.ErrMajorUpgradeCheckUnavailable):
		httpError(w, service.ErrMajorUpgradeCheckUnavailable.Error(), http.StatusServiceUnavailable)
	default:
		log.Printf("ERROR: major upgrade of %s: %v", projectID, err)
		httpError(w, "the major upgrade could not be started", http.StatusInternalServerError)
	}
}
