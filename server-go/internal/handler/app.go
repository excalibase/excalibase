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
// database. This is the resource model and its CRUD surface only; each app is
// shown with the URL it is served at, which the deploy creates.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const routeAppID = "/{appId}"

// maxAppBodyBytes bounds a create or update body before it is decoded. The
// variables alone may weigh 64 KiB, so the limit is twice that and no more:
// past it the request is refused rather than allocated.
const maxAppBodyBytes = 128 * 1024

// AppHandler exposes CRUD over a project's apps.
type AppHandler struct {
	store   apphost.Store
	project apphost.ProjectFacts
	route   apphost.Route
	// disks caps an app's disk by its organisation's plan; unset, no disk is accepted.
	disks apphost.DiskLimits
	// limits caps the project's app count by its plan; unset, no app is created.
	limits apphost.AppLimits
	budget *storagebudget.Budget
}

// SetAppLimits wires the plan's app count (EXC-524).
func (h *AppHandler) SetAppLimits(limits apphost.AppLimits) { h.limits = limits }

// SetDiskLimits wires the plan cap an app's disk is held to (EXC-523).
func (h *AppHandler) SetDiskLimits(disks apphost.DiskLimits) { h.disks = disks }

// SetStorageBudget refuses, when it is asked for, a disk the platform's storage cannot hold.
func (h *AppHandler) SetStorageBudget(budget *storagebudget.Budget) { h.budget = budget }

func NewAppHandler(store apphost.Store, project apphost.ProjectFacts, route apphost.Route) *AppHandler {
	return &AppHandler{store: store, project: project, route: route}
}

// appResponse adds the URL, derived rather than stored so a renamed app or a new domain never shows a stale one.
type appResponse struct {
	*apphost.App
	URL string `json:"url,omitempty"`
}

// present leaves the URL out when the app has no hostname; its deploy is what reports why.
func (h *AppHandler) present(app *apphost.App) appResponse {
	if app.Internal {
		return appResponse{App: app}
	}
	url, err := h.route.URL(app.Name, app.ProjectID)
	if err != nil {
		return appResponse{App: app}
	}
	return appResponse{App: app, URL: url}
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
	plans     ProjectPlans
}

// ProjectPlans answers which plan a project's organisation is on now.
type ProjectPlans interface {
	ProjectPlanTier(ctx context.Context, projectID string) (domain.TierType, error)
}

// NewProjectSourceLookup answers both questions an app write asks about the
// project it is being written to: which plan sizes it, and whether it exposes
// the source a reference names.
func NewProjectSourceLookup(instances storage.InstanceStore, plans ProjectPlans) apphost.ProjectFacts {
	return projectSources{instances: instances, plans: plans}
}

// Tier is the organisation's current plan: it sizes an app exactly as it
// sizes the project, and a caller never names one.
func (p projectSources) Tier(ctx context.Context, projectID string) (domain.TierType, error) {
	if p.plans == nil {
		return "", errors.New("no plan source")
	}
	return p.plans.ProjectPlanTier(ctx, projectID)
}

func (p projectSources) HasSource(projectID string, kind apphost.SourceKind, name string) (bool, error) {
	if kind != apphost.SourceDatabase {
		return false, nil
	}
	inst, err := p.instances.FindByProjectID(projectID)
	if err != nil {
		return false, err
	}
	// A database still being built is not a source yet: its address exists
	// only once provisioning has finished, so a reference to it now would be
	// a reference to nothing.
	if inst == nil || inst.DatabaseName == "" ||
		domain.IsNotServable(inst.Status) || domain.IsBuildingStatus(inst.Status) {
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
	Disk            *apphost.AppDisk `json:"disk"`
	// InternalPorts are optional raw TCP ports for the project's own apps (EXC-525).
	InternalPorts []apphost.InternalPort `json:"internalPorts"`
	// Internal makes the app a service with no HTTP port and no public route.
	Internal bool `json:"internal"`
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
	// Disk attaches a disk, or moves an attached one's mount path; its size
	// grows through POST .../disk, and null or absent leaves it as it is.
	Disk          *apphost.AppDisk        `json:"disk"`
	InternalPorts *[]apphost.InternalPort `json:"internalPorts"`
	Internal      *bool                   `json:"internal"`
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
	out := make([]appResponse, 0, len(apps))
	for _, app := range apps {
		out = append(out, h.present(app))
	}
	writeJSON(w, out)
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
	h.writeAppJSON(w, app)
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
	r.Body = http.MaxBytesReader(w, r.Body, maxAppBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	if req.Port == nil && !req.Internal {
		httpError(w, "port is required (1-65535) for a public web app", http.StatusBadRequest)
		return
	}
	if req.Replicas == nil {
		httpError(w, "replicas is required (0-3; 0 means stopped)", http.StatusBadRequest)
		return
	}

	tier, err := h.project.Tier(r.Context(), projectID)
	if err != nil {
		httpError(w, "could not read the project's tier", http.StatusInternalServerError)
		return
	}

	app := &apphost.App{
		ID:              uuid.NewString(),
		ProjectID:       projectID,
		Name:            strings.TrimSpace(req.Name),
		Image:           req.Image,
		Env:             normalizeEnv(req.Env),
		Port:            portOrNone(req.Port),
		Internal:        req.Internal,
		InternalPorts:   emptyAsNil(req.InternalPorts),
		HealthCheckPath: req.HealthCheckPath,
		Replicas:        *req.Replicas,
		Disk:            callerDisk(req.Disk, 0),
		Tier:            tier,
		Status:          apphost.StatusFor(*req.Replicas),
	}
	if err := app.Validate(); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !h.diskWithinPlan(w, r, app.ProjectID, app.Disk) {
		return
	}
	// A reference to a source the project does not have is fatal: it is never
	// created as an empty variable to be discovered at deploy time.
	if err := apphost.ValidateReferences(app, h.project); err != nil {
		h.writeReferenceError(w, err)
		return
	}
	maxApps, ok := h.planAppLimit(w, r, app.ProjectID)
	if !ok {
		return
	}
	if err := h.store.Create(app, maxApps); err != nil {
		h.writeStoreError(w, err)
		return
	}
	w.Header().Set("ETag", strconv.Itoa(app.Version))
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, h.present(app))
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
	expectedVersion, ok := appVersionPrecondition(w, r)
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
	if existing.Version != expectedVersion {
		httpError(w, appStaleMessage(existing.Version), http.StatusPreconditionFailed)
		return
	}

	var req appUpdateRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxAppBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	attaching := existing.Disk == nil && req.Disk != nil
	if existing.Disk != nil && req.Disk != nil && req.Disk.Size != existing.Disk.Size {
		httpError(w, "a disk's size changes only by growing it with POST .../disk", http.StatusBadRequest)
		return
	}
	applyAppUpdate(existing, req)
	// The tier follows the project, so an app never keeps an envelope the
	// project has moved off.
	tier, err := h.project.Tier(r.Context(), projectID)
	if err != nil {
		httpError(w, "could not read the project's tier", http.StatusInternalServerError)
		return
	}
	existing.Tier = tier
	if err := existing.Validate(); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if attaching && !h.diskWithinPlan(w, r, projectID, existing.Disk) {
		return
	}
	if err := apphost.ValidateReferences(existing, h.project); err != nil {
		h.writeReferenceError(w, err)
		return
	}
	if err := h.store.Update(existing, expectedVersion); err != nil {
		h.writeStoreError(w, err)
		return
	}
	h.writeAppJSON(w, existing)
}

// -------------------- helpers --------------------

// applyAppUpdate copies the fields the caller sent onto the stored record. The
// status is left alone: an edit changes what the next deploy runs, not what
// is running now.
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
	if req.Replicas != nil {
		app.Replicas = *req.Replicas
	}
	if req.InternalPorts != nil {
		app.InternalPorts = emptyAsNil(*req.InternalPorts)
	}
	if req.Internal != nil {
		app.Internal = *req.Internal
	}
	if req.Disk != nil {
		generation := 0
		if app.Disk != nil {
			generation = app.Disk.Generation
		}
		app.Disk = callerDisk(req.Disk, generation)
	}
}

// planAppLimit reads how many apps the organisation's plan allows; a limit
// that cannot be read refuses the create rather than admitting it unchecked.
func (h *AppHandler) planAppLimit(w http.ResponseWriter, r *http.Request, projectID string) (int, bool) {
	if h.limits == nil {
		httpError(w, "could not read the plan's app limit", http.StatusInternalServerError)
		return 0, false
	}
	limit, err := h.limits.MaxApps(r.Context(), projectID)
	if err != nil {
		log.Printf("app limit for %s: %v", projectID, err)
		httpError(w, "could not read the plan's app limit", http.StatusInternalServerError)
		return 0, false
	}
	return limit, true
}

// callerDisk takes only what a caller may say about a disk; the generation,
// which names the volume holding it, stays the platform's.
func callerDisk(requested *apphost.AppDisk, generation int) *apphost.AppDisk {
	if requested == nil {
		return nil
	}
	return &apphost.AppDisk{MountPath: requested.MountPath, Size: requested.Size, Generation: generation}
}

// diskWithinPlan refuses a disk above the organisation's plan, and one whose
// cap cannot be read: a disk is never admitted unchecked.
func (h *AppHandler) diskWithinPlan(w http.ResponseWriter, r *http.Request, projectID string, disk *apphost.AppDisk) bool {
	if disk == nil {
		return true
	}
	if h.disks == nil {
		httpError(w, "could not read the plan's app disk limit", http.StatusInternalServerError)
		return false
	}
	limit, err := h.disks.MaxDiskBytes(r.Context(), projectID)
	if err != nil {
		httpError(w, "could not read the plan's app disk limit", http.StatusInternalServerError)
		return false
	}
	if err := apphost.CheckDiskWithinPlan(disk, limit); err != nil {
		httpError(w, err.Error(), http.StatusConflict)
		return false
	}
	size, _ := disk.Bytes()
	if err := h.budget.Check(r.Context(), size, "an app disk ("+disk.Size+")"); err != nil {
		if errors.Is(err, storagebudget.ErrExceeded) {
			httpError(w, err.Error(), http.StatusConflict)
			return false
		}
		log.Printf("app disk: storage budget: %v", err)
		httpError(w, "could not read the platform's storage budget", http.StatusInternalServerError)
		return false
	}
	return true
}

// portOrNone: an internal service sends no HTTP port; validation decides whether that is allowed.
func portOrNone(port *int) int {
	if port == nil {
		return 0
	}
	return *port
}

// emptyAsNil stores "no internal ports" one way, so the record reads the same however it was cleared.
func emptyAsNil(ports []apphost.InternalPort) []apphost.InternalPort {
	if len(ports) == 0 {
		return nil
	}
	return ports
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

// writeAppJSON answers with the app and the version to send back with the
// next write, so a client never has to invent one.
func (h *AppHandler) writeAppJSON(w http.ResponseWriter, app *apphost.App) {
	w.Header().Set("ETag", strconv.Itoa(app.Version))
	writeJSON(w, h.present(app))
}

// appVersionPrecondition reads the version the caller claims to have read,
// from If-Match. An update without one is refused: the server cannot tell an
// informed write from one about to overwrite a change the caller never saw.
func appVersionPrecondition(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpError(w, "If-Match is required: send the version the app was read at",
			http.StatusPreconditionRequired)
		return 0, false
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 1 {
		httpError(w, "If-Match must be the version the app was read at", http.StatusBadRequest)
		return 0, false
	}
	return version, true
}

func appStaleMessage(current int) string {
	return "the app was changed by someone else; it is now at version " + strconv.Itoa(current)
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
	case errors.Is(err, apphost.ErrAppVersionConflict):
		httpError(w, err.Error(), http.StatusPreconditionFailed)
	case errors.Is(err, apphost.ErrAppLimitReached), errors.Is(err, apphost.ErrAppNameTaken),
		errors.Is(err, apphost.ErrAppBusy):
		httpError(w, err.Error(), http.StatusConflict)
	default:
		httpError(w, safeError(err), http.StatusInternalServerError)
	}
}
