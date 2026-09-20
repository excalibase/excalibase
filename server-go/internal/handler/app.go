// Package handler — customer applications (EXC-378).
//
// Mounted at /api/projects/{projectId}/apps. The project sub-router already
// validates auth and project membership and applies the role gate; this
// handler treats the path's projectId as authoritative and never trusts a
// project named in a body.
//
// An app is a container image the platform runs beside the project's database,
// but the two are independent services: nothing here reads, requires or
// implies a provisioned database, so a project may hold an app and no
// database. This is the resource model and its CRUD surface only — it renders
// no Kubernetes object (EXC-379), routes no traffic (EXC-383/384) and runs no
// deploy (EXC-386).
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const routeAppID = "/{appId}"

// AppHandler exposes CRUD over a project's apps.
type AppHandler struct {
	store   apphost.Store
	sources apphost.SourceLookup
}

func NewAppHandler(store apphost.Store, sources apphost.SourceLookup) *AppHandler {
	return &AppHandler{store: store, sources: sources}
}

// projectSources answers what a project exposes to a reference variable. The
// only source today is the project's own provisioned database, named by the
// database name the platform recorded for it: a project whose database does
// not exist yet, or is being torn down, exposes nothing — which is exactly
// what makes a reference to it fatal instead of empty.
//
// Nothing here connects to the database or reads an address from it. What a
// reference resolves to is rendered by EXC-379/386.
type projectSources struct {
	instances storage.InstanceStore
}

// NewProjectSourceLookup resolves reference targets against the project's
// provisioned database.
func NewProjectSourceLookup(instances storage.InstanceStore) apphost.SourceLookup {
	return projectSources{instances: instances}
}

func (p projectSources) HasSource(projectID string, kind apphost.SourceKind, name string) (bool, error) {
	if kind != apphost.SourceDatabase {
		return false, nil
	}
	inst, err := p.instances.FindByProjectID(projectID)
	if err != nil {
		return false, err
	}
	if inst == nil || inst.DatabaseName == "" || domain.IsNotServable(inst.Status) {
		return false, nil
	}
	return inst.DatabaseName == name, nil
}

// Routes registers the apps sub-tree under the project router.
func (h *AppHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Route(routeAppID, func(r chi.Router) {
		r.Get("/", h.Get)
		r.Patch("/", h.Update)
		r.Delete("/", h.Delete)
	})
}

// appCreateRequest is the create body. Every field is required except the
// health check path and the env set: replicas in particular is a pointer
// because zero is a meaningful request (an app created stopped), and reading
// an omitted field as zero would silently create a stopped app for a caller
// who simply forgot the field.
type appCreateRequest struct {
	Name            string           `json:"name"`
	Image           string           `json:"image"`
	Env             []apphost.EnvVar `json:"env"`
	Port            *int             `json:"port"`
	HealthCheckPath string           `json:"healthCheckPath"`
	Replicas        *int             `json:"replicas"`
	Tier            domain.TierType  `json:"tier"`
}

// appUpdateRequest is the partial-update body. Every field is a pointer so an
// omitted field keeps its stored value and an explicitly sent one — including
// an empty env set — is applied as sent.
type appUpdateRequest struct {
	Name            *string           `json:"name"`
	Image           *string           `json:"image"`
	Env             *[]apphost.EnvVar `json:"env"`
	Port            *int              `json:"port"`
	HealthCheckPath *string           `json:"healthCheckPath"`
	Replicas        *int              `json:"replicas"`
	Tier            *domain.TierType  `json:"tier"`
}

func (h *AppHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	apps, err := h.store.List(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if apps == nil {
		apps = []*apphost.App{}
	}
	writeJSON(w, apps)
}

func (h *AppHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	app, err := h.store.Get(projectID, appID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if app == nil {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}
	writeJSON(w, app)
}

// Create stores a new app. The id is assigned here rather than taken from the
// caller, and the status is derived from the replica count rather than sent:
// no caller may declare an app running.
func (h *AppHandler) Create(w http.ResponseWriter, r *http.Request) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return
	}
	var req appCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	if req.Port == nil {
		httpError(w, "port is required (1-65535)", http.StatusBadRequest)
		return
	}
	if req.Replicas == nil {
		httpError(w, "replicas is required (0-3; 0 means stopped)", http.StatusBadRequest)
		return
	}

	app := &apphost.App{
		ID:              uuid.NewString(),
		ProjectID:       projectID,
		Name:            strings.TrimSpace(req.Name),
		Image:           req.Image,
		Env:             normalizeEnv(req.Env),
		Port:            *req.Port,
		HealthCheckPath: req.HealthCheckPath,
		Replicas:        *req.Replicas,
		Tier:            req.Tier,
		Status:          apphost.StatusFor(*req.Replicas),
	}
	if err := app.Validate(); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	// A reference to a source the project does not have is fatal: it is never
	// created as an empty variable to be discovered at deploy time.
	if err := apphost.ValidateReferences(app, h.sources); err != nil {
		h.writeReferenceError(w, err)
		return
	}
	if err := h.store.Create(app); err != nil {
		h.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, app)
}

// Update applies a partial change onto the stored app. The body is read into
// a request of its own rather than decoded over the record: decoding an env
// array over the stored one would merge element by element, so swapping a
// literal value for a secret reference would leave the old value behind and
// produce an env var carrying both.
func (h *AppHandler) Update(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	existing, err := h.store.Get(projectID, appID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if existing == nil {
		httpError(w, errNotFound, http.StatusNotFound)
		return
	}

	var req appUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	applyAppUpdate(existing, req)
	if err := existing.Validate(); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := apphost.ValidateReferences(existing, h.sources); err != nil {
		h.writeReferenceError(w, err)
		return
	}
	if err := h.store.Update(existing); err != nil {
		h.writeStoreError(w, err)
		return
	}
	writeJSON(w, existing)
}

func (h *AppHandler) Delete(w http.ResponseWriter, r *http.Request) {
	projectID, appID, ok := h.appPath(w, r)
	if !ok {
		return
	}
	if err := h.store.Delete(projectID, appID); err != nil {
		h.writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// -------------------- helpers --------------------

// applyAppUpdate copies the fields the caller sent onto the stored record. The
// status follows the replica count, so a stop or a restart is recorded as what
// was asked for and never as an observation that the workload is running.
func applyAppUpdate(app *apphost.App, req appUpdateRequest) {
	if req.Name != nil {
		app.Name = strings.TrimSpace(*req.Name)
	}
	if req.Image != nil {
		app.Image = *req.Image
		// The stored digest belonged to the old image; the next deploy
		// resolves the new one (EXC-386).
		app.ResolvedDigest = ""
	}
	if req.Env != nil {
		app.Env = normalizeEnv(*req.Env)
	}
	if req.Port != nil {
		app.Port = *req.Port
	}
	if req.HealthCheckPath != nil {
		app.HealthCheckPath = *req.HealthCheckPath
	}
	if req.Tier != nil {
		app.Tier = *req.Tier
	}
	if req.Replicas != nil {
		app.Replicas = *req.Replicas
		app.Status = apphost.StatusFor(*req.Replicas)
	}
}

// normalizeEnv returns a non-nil slice so an emptied env set is stored as an
// empty set rather than as an absent one.
func normalizeEnv(env []apphost.EnvVar) []apphost.EnvVar {
	if env == nil {
		return []apphost.EnvVar{}
	}
	return env
}

// appPath returns the validated projectId and appId, or writes the refusal.
func (h *AppHandler) appPath(w http.ResponseWriter, r *http.Request) (string, string, bool) {
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

// writeReferenceError answers an unresolvable reference with the refusal that
// names it, and a failed lookup as an internal error — the two are different
// facts and must not read as one.
func (h *AppHandler) writeReferenceError(w http.ResponseWriter, err error) {
	if errors.Is(err, apphost.ErrUnresolvedReference) {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The lookup's own failure is wrapped around storage detail, which
	// safeError only strips from the front of a message — so the caller is
	// told what failed and nothing else.
	httpError(w, "could not check the app's references", http.StatusInternalServerError)
}

// writeStoreError maps the store's named failures onto status codes. The app
// limit and a duplicate name are conflicts — the request was well formed and
// the project's state refused it — while anything else is an internal error
// whose detail is not shown to the caller.
func (h *AppHandler) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apphost.ErrAppNotFound):
		httpError(w, errNotFound, http.StatusNotFound)
	case errors.Is(err, apphost.ErrAppLimitReached), errors.Is(err, apphost.ErrAppNameTaken):
		httpError(w, err.Error(), http.StatusConflict)
	default:
		httpError(w, safeError(err), http.StatusInternalServerError)
	}
}
