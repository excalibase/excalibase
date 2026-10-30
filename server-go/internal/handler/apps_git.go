// Package handler — git builds of apps (EXC-547): connect a repository to an
// app, the builds a push queues, and a build's log. The routes sit under
// /api/projects/{projectId}/apps/{appId} behind the project's access guard
// and the appgitbuilds flag; the webhook and sign-in callback are the sites' own.
package handler

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
)

// AppGitHandler serves git builds of apps.
type AppGitHandler struct {
	builds *sitebuilds.Service
}

func NewAppGitHandler(service *sitebuilds.Service) *AppGitHandler {
	return &AppGitHandler{builds: service}
}

// Routes mounts the per-app git routes inside /apps/{appId}. dark hides every
// route but the disconnect, which must work with the flag off; developer gates every change.
func (h *AppGitHandler) Routes(r chi.Router, developer, dark func(http.Handler) http.Handler) {
	r.With(dark).Get("/git", h.on(h.connection))
	r.With(dark, developer).Put("/git", h.on(h.connect))
	r.With(developer).Delete("/git", h.on(h.disconnect))
	r.With(dark, developer).Post("/git/sign-in", h.on(h.signIn))
	r.With(dark, developer).Get("/git/repositories", h.on(h.repositories))
	r.With(dark).Get("/builds", h.on(h.listBuilds))
	r.With(dark, developer).Post("/builds", h.on(h.rebuild))
	// A build log is developers only: build output can print what a viewer should not see.
	r.With(dark, developer).Get("/builds/{buildId}", h.on(h.getBuild))
}

func (h *AppGitHandler) on(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gitBuildsAvailable(w, r, h.builds, writeAppGitError) {
			return
		}
		next(w, r)
	}
}

func projectApp(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	appID, ok := sitePathID(w, r, "appId")
	return chi.URLParam(r, "projectId"), appID, ok
}

func (h *AppGitHandler) connection(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := projectApp(w, r)
	if !ok {
		return
	}
	connection, err := h.builds.AppConnection(r.Context(), projectID, appID)
	if err != nil {
		writeAppGitError(w, err)
		return
	}
	writeJSON(w, map[string]any{"connected": connection != nil, "connection": connection, "builders": sitebuilds.AppBuilders})
}

type appConnectBody struct {
	RepositoryID int64                  `json:"repositoryId"`
	Repository   string                 `json:"repository"`
	Settings     sitebuilds.AppSettings `json:"settings"`
}

func (h *AppGitHandler) connect(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := projectApp(w, r)
	if !ok {
		return
	}
	var body appConnectBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGitSettingsBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	result, err := h.builds.ConnectApp(r.Context(), sitebuilds.AppConnectRequest{ProjectID: projectID, AppID: appID, UserID: actorID(r),
		RepositoryID: body.RepositoryID, Repository: body.Repository, Settings: body.Settings})
	if err != nil {
		writeAppGitError(w, err)
		return
	}
	writeJSON(w, result)
}

func (h *AppGitHandler) disconnect(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := projectApp(w, r)
	if !ok {
		return
	}
	if err := h.builds.DisconnectApp(r.Context(), projectID, appID); err != nil {
		writeAppGitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AppGitHandler) signIn(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := projectApp(w, r)
	if !ok {
		return
	}
	install, ok := installRequested(w, r)
	if !ok {
		return
	}
	started, err := h.builds.StartAppSignIn(r.Context(), actorID(r), projectID, appID, install)
	if err != nil {
		writeAppGitError(w, err)
		return
	}
	http.SetCookie(w, siteGitCookie(started.BrowserKey, 600))
	writeJSON(w, map[string]string{"url": started.URL})
}

func (h *AppGitHandler) repositories(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := projectApp(w, r)
	if !ok {
		return
	}
	access, err := h.builds.AppRepositories(r.Context(), actorID(r), projectID, appID)
	if err != nil {
		writeAppGitError(w, err)
		return
	}
	writeJSON(w, access)
}

func (h *AppGitHandler) listBuilds(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := projectApp(w, r)
	if !ok {
		return
	}
	builds, err := h.builds.ListAppBuilds(r.Context(), projectID, appID)
	if err != nil {
		writeAppGitError(w, err)
		return
	}
	writeJSON(w, builds)
}

func (h *AppGitHandler) rebuild(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := projectApp(w, r)
	if !ok {
		return
	}
	build, err := h.builds.RebuildApp(r.Context(), projectID, appID, actorID(r))
	if err != nil {
		writeAppGitError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, build)
}

func (h *AppGitHandler) getBuild(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := projectApp(w, r)
	if !ok {
		return
	}
	buildID, ok := sitePathID(w, r, "buildId")
	if !ok {
		return
	}
	build, err := h.builds.GetAppBuild(r.Context(), projectID, appID, buildID)
	if err != nil {
		writeAppGitError(w, err)
		return
	}
	writeJSON(w, build)
}

// writeAppGitError maps the app refusals, then the shared git-build ones.
func writeAppGitError(w http.ResponseWriter, err error) {
	var limited *sitebuilds.AppBuildRateLimitError
	switch {
	case errors.Is(err, apphost.ErrAppNotFound):
		httpError(w, errNotFound, http.StatusNotFound)
	case errors.Is(err, sitebuilds.ErrAppBuildsOff):
		httpError(w, err.Error(), http.StatusServiceUnavailable)
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Ceil(limited.RetryAfter.Seconds())), 10))
		httpError(w, err.Error(), http.StatusTooManyRequests)
	default:
		writeSiteGitError(w, err)
	}
}
