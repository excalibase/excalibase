package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/go-chi/chi/v5"
)

type AppDeployer interface {
	DeployApp(ctx context.Context, projectID, appID, actor string) (*apphost.Deploy, error)
	ListDeploys(projectID, appID string, limit int) ([]*apphost.Deploy, error)
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

func (h *AppDeployHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrAppNotFound):
		httpError(w, errNotFound, http.StatusNotFound)
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
