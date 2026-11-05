// Package handler — git builds of sites (EXC-354): connect a repository to a
// site, the builds a push queues, and the GitHub webhook and sign-in callback.
//
// The site routes sit under /api/projects/{projectId}/sites behind the
// project's access guard. While SITES_GIT_BUILDS_ENABLED is off every route
// answers 404, as if unmounted; on single-host they answer 503 saying why.
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// siteGitBrowserCookie binds a git-host sign-in to the browser that started it.
// Lax, because the git host returns the browser with a cross-site navigation.
const (
	siteGitBrowserCookie = "excali_site_git"
	siteGitCookiePath    = "/api/sites/github"
)

const (
	maxGitSettingsBytes = 128 << 10
	// maxWebhookBytes bounds an unauthenticated body before its signature is checked.
	maxWebhookBytes = 5 << 20
)

// SiteGitHandler serves git builds of sites.
type SiteGitHandler struct {
	builds    *sitebuilds.Service
	studioURL string
}

func NewSiteGitHandler(service *sitebuilds.Service, studioURL string) *SiteGitHandler {
	return &SiteGitHandler{builds: service, studioURL: studioURL}
}

// Routes mounts the per-site git routes beside the site tree; developer gates every change.
func (h *SiteGitHandler) Routes(r chi.Router, developer func(http.Handler) http.Handler) {
	r.With(developer).Get("/build-secrets", h.on(h.listSecrets))
	r.With(developer).Put("/build-secrets", h.on(h.setSecrets))
	r.Get("/{siteId}/git", h.on(h.connection))
	r.With(developer).Put("/{siteId}/git", h.on(h.connect))
	r.With(developer).Delete("/{siteId}/git", h.on(h.disconnect))
	r.With(developer).Post("/{siteId}/git/sign-in", h.on(h.signIn))
	r.With(developer).Get("/{siteId}/git/repositories", h.on(h.repositories))
	r.Get("/{siteId}/builds", h.on(h.listBuilds))
	r.With(developer).Post("/{siteId}/builds", h.on(h.rebuild))
	// A build log is developers only: build output can print what a viewer should not see.
	r.With(developer).Get("/{siteId}/builds/{buildId}", h.on(h.getBuild))
}

// PublicRoutes are the git host's: the webhook (its signature is the credential)
// and the sign-in callback (the single-use state is).
func (h *SiteGitHandler) PublicRoutes(r chi.Router) {
	r.Post("/webhook", h.on(h.webhook))
	r.Get("/callback", h.on(h.callback))
}

func (h *SiteGitHandler) on(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !gitBuildsAvailable(w, r, h.builds, writeSiteGitError) {
			return
		}
		next(w, r)
	}
}

// gitBuildsAvailable answers 404 while git builds are off and 503 on single-host, before anything else runs.
func gitBuildsAvailable(w http.ResponseWriter, r *http.Request, builds *sitebuilds.Service, refuse func(http.ResponseWriter, error)) bool {
	err := builds.Available()
	if errors.Is(err, sitebuilds.ErrUnavailable) {
		http.NotFound(w, r)
		return false
	}
	if err != nil {
		refuse(w, err)
		return false
	}
	return true
}

func projectSite(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	siteID, ok := sitePathID(w, r, "siteId")
	return chi.URLParam(r, "projectId"), siteID, ok
}

func (h *SiteGitHandler) connection(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	connection, err := h.builds.Connection(r.Context(), projectID, siteID)
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	writeJSON(w, map[string]any{"connected": connection != nil, "connection": connection, "nodeVersions": sitebuilds.NodeVersions})
}

type connectBody struct {
	RepositoryID int64               `json:"repositoryId"`
	Repository   string              `json:"repository"`
	Settings     sitebuilds.Settings `json:"settings"`
}

func (h *SiteGitHandler) connect(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	var body connectBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGitSettingsBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	result, err := h.builds.Connect(r.Context(), sitebuilds.ConnectRequest{ProjectID: projectID, SiteID: siteID, UserID: actorID(r),
		RepositoryID: body.RepositoryID, Repository: body.Repository, Settings: body.Settings})
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	writeJSON(w, result)
}

func (h *SiteGitHandler) disconnect(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	if err := h.builds.Disconnect(r.Context(), projectID, siteID); err != nil {
		writeSiteGitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *SiteGitHandler) signIn(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	install, ok := installRequested(w, r)
	if !ok {
		return
	}
	started, err := h.builds.StartSignIn(r.Context(), actorID(r), projectID, siteID, install)
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	http.SetCookie(w, siteGitCookie(started.BrowserKey, 600))
	writeJSON(w, map[string]string{"url": started.URL})
}

// installRequested reads the optional {"install": true} a sign-in start may carry.
func installRequested(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var body struct {
		Install bool `json:"install"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSiteBodyBytes)).Decode(&body); err != nil {
			httpError(w, errInvalidJSON, http.StatusBadRequest)
			return false, false
		}
	}
	return body.Install, true
}

func (h *SiteGitHandler) repositories(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	access, err := h.builds.Repositories(r.Context(), actorID(r), projectID, siteID)
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	writeJSON(w, access)
}

func (h *SiteGitHandler) listBuilds(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	builds, err := h.builds.ListBuilds(r.Context(), projectID, siteID)
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	writeJSON(w, builds)
}

func (h *SiteGitHandler) rebuild(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	build, err := h.builds.Rebuild(r.Context(), projectID, siteID, actorID(r))
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, build)
}

func (h *SiteGitHandler) getBuild(w http.ResponseWriter, r *http.Request) {
	projectID, siteID, ok := projectSite(w, r)
	if !ok {
		return
	}
	buildID, ok := sitePathID(w, r, "buildId")
	if !ok {
		return
	}
	build, err := h.builds.GetBuild(r.Context(), projectID, siteID, buildID)
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	writeJSON(w, build)
}

func (h *SiteGitHandler) listSecrets(w http.ResponseWriter, r *http.Request) {
	listed, err := h.builds.SecretVisibility(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	writeJSON(w, listed)
}

func (h *SiteGitHandler) setSecrets(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BuildVisible []string `json:"buildVisible"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGitSettingsBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	listed, err := h.builds.SetSecretVisibility(r.Context(), chi.URLParam(r, "projectId"), body.BuildVisible, actorID(r))
	if err != nil {
		writeSiteGitError(w, err)
		return
	}
	writeJSON(w, listed)
}

// webhook answers 202 for a delivery it accepted, whether or not a site tracks the push.
func (h *SiteGitHandler) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		httpError(w, "the delivery is too large", http.StatusRequestEntityTooLarge)
		return
	}
	queued, err := h.builds.HandleWebhook(r.Context(), r.Header, body)
	if errors.Is(err, gitprovider.ErrBadSignature) {
		httpError(w, "signature does not match", http.StatusUnauthorized)
		return
	}
	if err != nil {
		log.Printf("site builds: webhook %s: %v", safeLog(r.Header.Get("X-GitHub-Delivery")), err)
		httpError(w, "the delivery could not be processed", http.StatusInternalServerError)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]int{"queued": queued})
}

// callback finishes a sign-in and sends the browser back to the site's or app's page in Studio.
func (h *SiteGitHandler) callback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, siteGitCookie("", -1))
	var browserKey string
	if cookie, err := r.Cookie(siteGitBrowserCookie); err == nil {
		browserKey = cookie.Value
	}
	query := r.URL.Query()
	signIn, err := h.builds.FinishSignIn(r.Context(), query.Get("state"), browserKey, query.Get("code"))
	if signIn.ProjectID == "" {
		message := sitebuilds.ErrStateInvalid.Error()
		if err != nil && !errors.Is(err, sitebuilds.ErrStateInvalid) {
			log.Printf("site builds: sign-in callback: %v", err)
		}
		httpError(w, message, http.StatusBadRequest)
		return
	}
	outcome := url.Values{"git": {"signed-in"}}
	if err != nil {
		log.Printf("git builds: sign-in for site %q app %q: %v", signIn.SiteID, signIn.AppID, err)
		outcome = url.Values{"git": {"error"}}
	}
	http.Redirect(w, r, signInReturn(h.studioURL, signIn, outcome), http.StatusFound)
}

// signInReturn is the Studio page a sign-in started from: an app's container page, or a site's.
func signInReturn(studioURL string, signIn sitebuilds.SignIn, outcome url.Values) string {
	page := "/sites/" + url.PathEscape(signIn.SiteID)
	if signIn.AppID != "" {
		page = "/containers/" + url.PathEscape(signIn.AppID)
	}
	return studioURL + "/project/" + url.PathEscape(signIn.ProjectID) + page + "?" + outcome.Encode()
}

func siteGitCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: siteGitBrowserCookie, Value: value, Path: siteGitCookiePath, MaxAge: maxAge,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

// writeSiteGitError maps the git-build refusals, then the Sites ones.
func writeSiteGitError(w http.ResponseWriter, err error) {
	var input *sitebuilds.InputError
	switch {
	case errors.Is(err, sitebuilds.ErrSingleHost), errors.Is(err, sitebuilds.ErrUnavailable):
		httpError(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, sitebuilds.ErrNotConnected), errors.Is(err, sitebuilds.ErrBuildNotFound):
		httpError(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, sitebuilds.ErrNotAuthorized), errors.Is(err, sitebuilds.ErrNotDeveloper):
		httpError(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, sitebuilds.ErrConnectionInactive):
		httpError(w, sitebuilds.ErrConnectionInactive.Error(), http.StatusConflict)
	case errors.Is(err, sitebuilds.ErrStateInvalid), errors.As(err, &input):
		httpError(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, sites.ErrDeployRateLimited):
		w.Header().Set("Retry-After", "60")
		httpError(w, err.Error(), http.StatusTooManyRequests)
	default:
		writeSiteError(w, err)
	}
}
