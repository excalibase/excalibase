package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/security"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

const routeNameParam = "/{name}"


type SetupHandler struct{ svc *service.OperatorSetupService }

func NewSetupHandler(svc *service.OperatorSetupService) *SetupHandler { return &SetupHandler{svc: svc} }

// Routes mounts the setup surface. statusLimits is the per-IP limiter the
// anonymous status poll carries — the mount supplies it, because a route that
// takes no credential has nothing else bounding how often it is called.
func (h *SetupHandler) Routes(r chi.Router, statusLimits ...func(http.Handler) http.Handler) {
	// The installer polls the status before any credential exists, so it takes
	// no credential — and answers a single boolean: which operators are
	// installed, and in what version, is cluster topology an anonymous caller
	// has no business learning (EXC-418).
	r.With(statusLimits...).Get("/status", h.GetStatus)
	// Installing a DB operator applies a remote YAML cluster-wide — restrict to
	// platform_admin (manage_setup), not any authenticated user (SEC-H5).
	r.With(auth.RequireAuth, auth.RequirePermission(auth.PermManageSetup)).
		Post("/install/{databaseType}", h.Install)
}

func (h *SetupHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, domain.SetupStatusResponse{Complete: h.svc.SetupComplete(r.Context())})
}

func (h *SetupHandler) Install(w http.ResponseWriter, r *http.Request) {
	dbType := domain.DatabaseType(chi.URLParam(r, "databaseType"))
	if err := h.svc.InstallOperator(r.Context(), dbType); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "installed", "type": string(dbType)})
}

type ParameterGroupHandler struct{ store interface {
	Save(pg *domain.ParameterGroup) error
	FindByName(name string) (*domain.ParameterGroup, error)
	FindAll() ([]*domain.ParameterGroup, error)
	Delete(name string) error
}}

func NewParameterGroupHandler(store interface {
	Save(pg *domain.ParameterGroup) error
	FindByName(name string) (*domain.ParameterGroup, error)
	FindAll() ([]*domain.ParameterGroup, error)
	Delete(name string) error
}) *ParameterGroupHandler {
	return &ParameterGroupHandler{store: store}
}

// errInvalidParamGroupName is the fixed answer for a rejected name — it never
// echoes the name back into the response.
const errInvalidParamGroupName = "invalid parameter group name"

func (h *ParameterGroupHandler) Routes(r chi.Router) {
	// Reads stay open to any authenticated caller (the provision page's group
	// selector). Parameter groups are global, cluster-wide configuration, so
	// writing one is a platform-admin act — the same tier as installing an
	// operator above.
	admin := auth.RequirePermission(auth.PermManageSetup)
	r.Get("/", h.List)
	r.With(admin).Post("/", h.Create)
	r.Get(routeNameParam, h.Get)
	r.With(admin).Put(routeNameParam, h.Update)
	r.With(admin).Delete(routeNameParam, h.Delete)
}

// paramGroupName validates the {name} path parameter at the handler boundary,
// before it can reach a path join in the store.
func paramGroupName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := chi.URLParam(r, "name")
	if err := security.ValidateIdentifier(name); err != nil {
		httpError(w, errInvalidParamGroupName, http.StatusBadRequest)
		return "", false
	}
	return name, true
}

// decodeParameterGroup reads the request body, refusing a malformed one
// instead of continuing with a zero-valued group.
func decodeParameterGroup(w http.ResponseWriter, r *http.Request) (domain.ParameterGroup, bool) {
	var pg domain.ParameterGroup
	if err := json.NewDecoder(r.Body).Decode(&pg); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return domain.ParameterGroup{}, false
	}
	return pg, true
}

func (h *ParameterGroupHandler) List(w http.ResponseWriter, r *http.Request) {
	all, err := h.store.FindAll()
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, all)
}

func (h *ParameterGroupHandler) Get(w http.ResponseWriter, r *http.Request) {
	name, ok := paramGroupName(w, r)
	if !ok {
		return
	}
	pg, err := h.store.FindByName(name)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if pg == nil {
		httpError(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, pg)
}

func (h *ParameterGroupHandler) Create(w http.ResponseWriter, r *http.Request) {
	pg, ok := decodeParameterGroup(w, r)
	if !ok {
		return
	}
	if err := security.ValidateIdentifier(pg.Name); err != nil {
		httpError(w, errInvalidParamGroupName, http.StatusBadRequest)
		return
	}
	if err := h.store.Save(&pg); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, pg)
}

func (h *ParameterGroupHandler) Update(w http.ResponseWriter, r *http.Request) {
	name, ok := paramGroupName(w, r)
	if !ok {
		return
	}
	pg, ok := decodeParameterGroup(w, r)
	if !ok {
		return
	}
	pg.Name = name
	if err := h.store.Save(&pg); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, pg)
}

func (h *ParameterGroupHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name, ok := paramGroupName(w, r)
	if !ok {
		return
	}
	if err := h.store.Delete(name); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}
