package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
	"github.com/go-chi/chi/v5"
)

type AppDeployer interface {
	DeployAppAs(ctx context.Context, projectID, appID string, origin apphost.DeployOrigin) (*apphost.Deploy, error)
	DeployImage(ctx context.Context, projectID, appID, image string, origin apphost.DeployOrigin) (*apphost.Deploy, error)
	RedeployAppAs(ctx context.Context, projectID, appID, deployID string, origin apphost.DeployOrigin) (*apphost.Deploy, error)
	GetDeploy(projectID, appID, deployID string) (*apphost.Deploy, error)
	ListDeploys(projectID, appID string, limit int) ([]*apphost.Deploy, error)
	PauseApp(ctx context.Context, projectID, appID string) (*apphost.App, error)
	ResumeApp(ctx context.Context, projectID, appID, actor string) (*apphost.App, error)
	DeleteApp(ctx context.Context, projectID, appID string, confirmDeleteDisk bool) error
	PauseAppInBackground(ctx context.Context, projectID, appID string) (*apphost.App, error)
	ResumeAppInBackground(ctx context.Context, projectID, appID, actor string) (*apphost.App, error)
	DeleteAppInBackground(ctx context.Context, projectID, appID string, confirmDeleteDisk bool) (*apphost.App, error)
	ResizeAppDisk(ctx context.Context, projectID, appID, size string) (*apphost.App, error)
	AppDiskStatus(ctx context.Context, projectID, appID string) (*service.AppDiskReport, error)
}

type AppDeployHandler struct {
	deploys AppDeployer
}

func NewAppDeployHandler(deploys AppDeployer) *AppDeployHandler {
	return &AppDeployHandler{deploys: deploys}
}

// deployRequest is the optional body of POST .../deploy. With an image, the
// deploy resolves it to a digest and runs that digest (EXC-543); without one
// it runs the app as it is.
type deployRequest struct {
	Image     string `json:"image"`
	CommitSHA string `json:"commitSha"`
}

const maxDeployBodyBytes = 4096

// deployView is a deploy as the API answers it, with the address it serves on
// at the top so a CI job reads it without knowing the spec.
type deployView struct {
	*apphost.Deploy
	URL string `json:"url,omitempty"`
}

func newDeployView(deploy *apphost.Deploy) deployView {
	return deployView{Deploy: deploy, URL: deploy.Spec.URL}
}

func (h *AppDeployHandler) Deploy(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	body, err := decodeDeployRequest(w, r)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	origin := apphost.DeployOrigin{Actor: actorID(r), Source: deploySource(r), CommitSHA: body.CommitSHA}
	var deploy *apphost.Deploy
	if body.Image == "" {
		deploy, err = h.deploys.DeployAppAs(r.Context(), projectID, appID, origin)
	} else {
		deploy, err = h.deploys.DeployImage(r.Context(), projectID, appID, body.Image, origin)
	}
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, newDeployView(deploy))
}

// decodeDeployRequest refuses a field it does not know: a misspelt image must
// not quietly deploy whatever the app already runs.
func decodeDeployRequest(w http.ResponseWriter, r *http.Request) (deployRequest, error) {
	var body deployRequest
	if r.Body == nil {
		return body, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDeployBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return body, errors.New(`the body must be JSON such as {"image":"ghcr.io/acme/web:main","commitSha":"<git sha>"}, or empty`)
	}
	if err := apphost.ValidateCommitSHA(body.CommitSHA); err != nil {
		return body, err
	}
	return body, nil
}

// deploySource tells a deploy asked from Studio, which signs in with a
// session, from one asked with a token.
func deploySource(r *http.Request) string {
	if auth.IsSessionToken(auth.GetToken(r.Context())) {
		return apphost.DeploySourceStudio
	}
	return apphost.DeploySourceAPI
}

func (h *AppDeployHandler) Redeploy(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	deployID, ok := deployIDFromPath(w, r)
	if !ok {
		return
	}
	origin := apphost.DeployOrigin{Actor: actorID(r), Source: deploySource(r)}
	deploy, err := h.deploys.RedeployAppAs(r.Context(), projectID, appID, deployID, origin)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, newDeployView(deploy))
}

// GetDeploy is what a CI job polls until the deploy is succeeded, failed or superseded.
func (h *AppDeployHandler) GetDeploy(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	deployID, ok := deployIDFromPath(w, r)
	if !ok {
		return
	}
	deploy, err := h.deploys.GetDeploy(projectID, appID, deployID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, newDeployView(deploy))
}

func deployIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	deployID := chi.URLParam(r, "deployId")
	if err := apphost.ValidateID(deployID); err != nil {
		httpError(w, "invalid deployId", http.StatusBadRequest)
		return "", false
	}
	return deployID, true
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
	if prefersRespondAsync(r) {
		h.lifecycleAccepted(w, r, h.deploys.PauseAppInBackground)
		return
	}
	h.lifecycle(w, r, h.deploys.PauseApp)
}

func (h *AppDeployHandler) Resume(w http.ResponseWriter, r *http.Request) {
	if prefersRespondAsync(r) {
		h.lifecycleAccepted(w, r, func(ctx context.Context, projectID, appID string) (*apphost.App, error) {
			return h.deploys.ResumeAppInBackground(ctx, projectID, appID, actorID(r))
		})
		return
	}
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
	if prefersRespondAsync(r) {
		h.lifecycleAccepted(w, r, func(ctx context.Context, projectID, appID string) (*apphost.App, error) {
			return h.deploys.DeleteAppInBackground(ctx, projectID, appID, body.ConfirmDeleteDisk)
		})
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

// lifecycleAccepted answers 202 once the operation is started (RFC 7240
// respond-async). acceptedAt lets the caller tell a failure the app records
// afterwards from an older one.
func (h *AppDeployHandler) lifecycleAccepted(w http.ResponseWriter, r *http.Request,
	start func(context.Context, string, string) (*apphost.App, error)) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	acceptedAt := time.Now().UTC()
	app, err := start(r.Context(), projectID, appID)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"id": app.ID, "status": app.Status, "acceptedAt": acceptedAt})
}

// registryRetryAfter is how long a caller waits when the registry rate limits us.
const registryRetryAfter = "60"

func (h *AppDeployHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrInvalidImage):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, imagedigest.ErrNotFound), errors.Is(err, imagedigest.ErrDenied),
		errors.Is(err, imagedigest.ErrNotPublic):
		httpError(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, imagedigest.ErrRateLimited):
		w.Header().Set("Retry-After", registryRetryAfter)
		httpError(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, imagedigest.ErrUnavailable):
		httpError(w, err.Error(), http.StatusBadGateway)
	case errors.Is(err, apphost.ErrAppVersionConflict):
		httpError(w, err.Error(), http.StatusConflict)
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
