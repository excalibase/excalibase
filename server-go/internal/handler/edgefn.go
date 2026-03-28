package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

type EdgeFnHandler struct {
	store  *edgefn.ScriptStore
	client *edgefn.RuntimeClient
}

func NewEdgeFnHandler(store *edgefn.ScriptStore, client *edgefn.RuntimeClient) *EdgeFnHandler {
	return &EdgeFnHandler{store: store, client: client}
}

func (h *EdgeFnHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Get("/runtime/status", h.RuntimeStatus)
	r.Route("/{fnId}", func(r chi.Router) {
		r.Get("/", h.Get)
		r.Delete("/", h.Delete)
		r.Post("/invoke", h.Invoke)
	})
}

func (h *EdgeFnHandler) List(w http.ResponseWriter, r *http.Request) {
	hookType := r.URL.Query().Get("hookType")
	scripts, _ := h.store.List(hookType)
	writeJSON(w, scripts)
}

func (h *EdgeFnHandler) Create(w http.ResponseWriter, r *http.Request) {
	var script edgefn.Script
	if err := json.NewDecoder(r.Body).Decode(&script); err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return
	}
	script.Active = true

	if err := h.store.Save(&script); err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Deploy to Deno runtime
	h.client.Deploy(r.Context(), script.ID, script.Code)

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, script)
}

func (h *EdgeFnHandler) Get(w http.ResponseWriter, r *http.Request) {
	fnId := chi.URLParam(r, "fnId")
	script, _ := h.store.Get(fnId)
	if script == nil {
		httpError(w, "function not found", http.StatusNotFound)
		return
	}
	writeJSON(w, script)
}

func (h *EdgeFnHandler) Delete(w http.ResponseWriter, r *http.Request) {
	fnId := chi.URLParam(r, "fnId")
	h.client.Delete(r.Context(), fnId)
	h.store.Delete(fnId)
	writeJSON(w, map[string]string{"status": "deleted", "id": fnId})
}

func (h *EdgeFnHandler) Invoke(w http.ResponseWriter, r *http.Request) {
	fnId := chi.URLParam(r, "fnId")

	script, _ := h.store.Get(fnId)
	if script == nil {
		httpError(w, "function not found", http.StatusNotFound)
		return
	}

	// Ensure deployed
	h.client.Deploy(r.Context(), script.ID, script.Code)

	var data interface{}
	json.NewDecoder(r.Body).Decode(&data)

	result, err := h.client.Invoke(r.Context(), fnId, data)
	if err != nil {
		httpError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, result)
}

func (h *EdgeFnHandler) RuntimeStatus(w http.ResponseWriter, r *http.Request) {
	healthy, err := h.client.Health(r.Context())
	status := "healthy"
	if err != nil || !healthy {
		status = "unavailable"
	}
	writeJSON(w, map[string]interface{}{
		"status":  status,
		"healthy": healthy,
	})
}
