package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

type AppDeployer interface {
	DeployApp(ctx context.Context, projectID, appID, actor string) (*apphost.Deploy, error)
	RedeployApp(ctx context.Context, projectID, appID, deployID, actor string) (*apphost.Deploy, error)
	ListDeploys(projectID, appID string, limit int) ([]*apphost.Deploy, error)
	PauseApp(ctx context.Context, projectID, appID string) (*apphost.App, error)
	ResumeApp(ctx context.Context, projectID, appID, actor string) (*apphost.App, error)
	DeleteApp(ctx context.Context, projectID, appID string, confirmDeleteDisk bool) error
	ResizeAppDisk(ctx context.Context, projectID, appID, size string) (*apphost.App, error)
	AppDiskStatus(ctx context.Context, projectID, appID string) (*service.AppDiskReport, error)
}

type AppDeployHandler struct {
	deploys AppDeployer
}

func NewAppDeployHandler(deploys AppDeployer) *AppDeployHandler {
	return &AppDeployHandler{deploys: deploys}
}

func (h *AppDeployHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	actor := actorID(r)
	deploy, err := h.deploys.DeployApp(r.Context(), projectID, appID, actor)
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, deploy)
}

func (h *AppDeployHandler) Redeploy(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	deployID := chi.URLParam(r, "deployId")
	if err := apphost.ValidateID(deployID); err != nil {
		httpError(w, "invalid deployId", http.StatusBadRequest)
		return
	}
	actor := actorID(r)
	deploy, err := h.deploys.RedeployApp(r.Context(), projectID, appID, deployID, actor)
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, deploy)
}

func (h *AppDeployHandler) ListDeploys(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	limit, ok := deployListLimit(w, r)
	if !ok {
		return
	}
	deploys, err := h.deploys.ListDeploys(projectID, appID, limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, deploys)
}

const maxDeployListLimit = 200

func deployListLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maxDeployListLimit {
		httpError(w, "limit must be between 1 and "+strconv.Itoa(maxDeployListLimit), http.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func (h *AppDeployHandler) appPath(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return "", "", false
	}
	appID := chi.URLParam(r, "appId")
	if err := apphost.ValidateID(appID); err != nil {
		httpError(w, "invalid appId", http.StatusBadRequest)
		return "", "", false
	}
	return projectID, appID, true
}

func (h *AppDeployHandler) Pause(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.deploys.PauseApp)
}

func (h *AppDeployHandler) Resume(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, func(ctx context.Context, projectID, appID string) (*apphost.App, error) {
		return h.deploys.ResumeApp(ctx, projectID, appID, actorID(r))
	})
}

// appDeleteBody is the optional DELETE body; an app with a disk is deleted
// only with confirmDeleteDisk, since its data goes with it.
type appDeleteBody struct {
	ConfirmDeleteDisk bool `json:"confirmDeleteDisk"`
}

// Delete answers once the app's pods are gone and the app is forgotten, not when the deletion was asked for.
func (h *AppDeployHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	var body appDeleteBody
	if err := decodeOptionalJSON(w, r, &body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	if err := h.deploys.DeleteApp(r.Context(), projectID, appID, body.ConfirmDeleteDisk); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type appDiskResizeBody struct {
	Size string `json:"size"`
}

// ResizeDisk grows the app's disk up to its plan's cap, or lowers a stopped
// app's disk to any size at or above what it holds (whole Mi or Gi).
func (h *AppDeployHandler) ResizeDisk(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	var body appDiskResizeBody
	r.Body = http.MaxBytesReader(w, r.Body, maxAppDiskBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Size == "" {
		httpError(w, "the body must name the new size, such as {\"size\":\"10Gi\"} or {\"size\":\"500Mi\"}", http.StatusBadRequest)
		return
	}
	app, err := h.deploys.ResizeAppDisk(r.Context(), projectID, appID, body.Size)
	if err != nil {
		h.writeDiskError(w, err)
		return
	}
	writeJSON(w, map[string]any{"id": app.ID, "disk": app.Disk})
}

// DiskStatus measures the app's disk: what it holds against its size and the plan's cap.
func (h *AppDeployHandler) DiskStatus(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	report, err := h.deploys.AppDiskStatus(r.Context(), projectID, appID)
	if err != nil {
		h.writeDiskError(w, err)
		return
	}
	writeJSON(w, report)
}

const maxAppDiskBodyBytes = 1024

// decodeOptionalJSON reads a small optional body: empty is the zero value, malformed is an error.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, into any) error {
	if r.Body == nil {
		return nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAppDiskBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(into); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (h *AppDeployHandler) writeDiskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrInvalidDisk), errors.Is(err, service.ErrAppDiskSameSize):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, apphost.ErrDiskAbovePlan), errors.Is(err, service.ErrAppHasNoDisk),
		errors.Is(err, k8s.ErrAppDiskNotExpandable), errors.Is(err, service.ErrAppDiskBelowUsage),
		errors.Is(err, service.ErrAppDiskLowerNeedsStop), errors.Is(err, service.ErrAppDiskUsageAbovePlan),
		errors.Is(err, storagebudget.ErrExceeded), errors.Is(err, k8s.ErrAppDiskUsageUnavailable):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, k8s.ErrAppDiskJob):
		log.Printf("app disk: %v", err)
		httpError(w, err.Error(), http.StatusBadGateway)
	case errors.Is(err, apphost.ErrAppNotFound), errors.Is(err, storage.ErrProjectBusy),
		errors.Is(err, service.ErrOrgTierUnresolved):
		h.writeError(w, err)
	default:
		log.Printf("app disk: %v", err)
		httpError(w, "the disk operation did not complete; retry the request", http.StatusInternalServerError)
	}
}

func (h *AppDeployHandler) lifecycle(w http.ResponseWriter, r *http.Request,
	op func(context.Context, string, string) (*apphost.App, error)) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	app, err := op(r.Context(), projectID, appID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, map[string]string{"id": app.ID, "status": app.Status})
}

func (h *AppDeployHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrAppNotFound), errors.Is(err, apphost.ErrDeployNotFound):
		httpError(w, errNotFound, http.StatusNotFound)
	case errors.Is(err, apphost.ErrAppStatusConflict), errors.Is(err, apphost.ErrAppBusy),
		errors.Is(err, storage.ErrProjectBusy),
		errors.Is(err, k8s.ErrAppNotDeployed), errors.Is(err, k8s.ErrAppNotPaused):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, k8s.ErrAppPodsRemain):
		httpError(w, err.Error()+"; retry to finish", http.StatusGatewayTimeout)
	case errors.Is(err, k8s.ErrAppRollout):
		httpError(w, err.Error(), http.StatusBadGateway)
	case errors.Is(err, service.ErrAppOverPlan), errors.Is(err, service.ErrAppDiskDeleteUnconfirmed),
		errors.Is(err, service.ErrAppDiskUsageAbovePlan), errors.Is(err, apphost.ErrDiskAbovePlan),
		errors.Is(err, storagebudget.ErrExceeded):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, k8s.ErrAppDiskJob):
		httpError(w, err.Error(), http.StatusBadGateway)
	case errors.Is(err, service.ErrOrgTierUnresolved):
		httpError(w, service.ErrOrgTierUnresolved.Error(), http.StatusInternalServerError)
	case errors.Is(err, service.ErrAppCapacity):
		httpError(w, service.ErrAppCapacity.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, service.ErrAppNoSandboxNode):
		httpError(w, service.ErrAppNoSandboxNode.Error(), http.StatusServiceUnavailable)
	default:
		httpError(w, safeError(err), http.StatusInternalServerError)
	}
}

func actorID(r *http.Request) string {
	if user := auth.GetUser(r.Context()); user != nil && user.ID != "" {
		return user.ID
	}
	return "unknown"
}
