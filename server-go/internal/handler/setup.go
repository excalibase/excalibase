package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

const routeNameParam = "/{name}"


type SetupHandler struct{ svc *service.OperatorSetupService }

func NewSetupHandler(svc *service.OperatorSetupService) *SetupHandler { return &SetupHandler{svc: svc} }

func (h *SetupHandler) Routes(r chi.Router) {
	r.Get("/status", h.GetStatus)
	r.Post("/install/{databaseType}", h.Install)
}

func (h *SetupHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.svc.GetStatus(r.Context()))
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

func (h *ParameterGroupHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get(routeNameParam, h.Get)
	r.Put(routeNameParam, h.Update)
	r.Delete(routeNameParam, h.Delete)
}

func (h *ParameterGroupHandler) List(w http.ResponseWriter, r *http.Request) {
	all, _ := h.store.FindAll()
	writeJSON(w, all)
}

func (h *ParameterGroupHandler) Get(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	pg, _ := h.store.FindByName(name)
	if pg == nil { httpError(w, "not found", http.StatusNotFound); return }
	writeJSON(w, pg)
}

func (h *ParameterGroupHandler) Create(w http.ResponseWriter, r *http.Request) {
	var pg domain.ParameterGroup
	json.NewDecoder(r.Body).Decode(&pg)
	if err := h.store.Save(&pg); err != nil { httpError(w, safeError(err), http.StatusInternalServerError); return }
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, pg)
}

func (h *ParameterGroupHandler) Update(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var pg domain.ParameterGroup
	json.NewDecoder(r.Body).Decode(&pg)
	pg.Name = name
	h.store.Save(&pg)
	writeJSON(w, pg)
}

func (h *ParameterGroupHandler) Delete(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	h.store.Delete(name)
	writeJSON(w, map[string]string{"status": "deleted"})
}
