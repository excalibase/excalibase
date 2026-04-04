package handler

import (
	"encoding/json"
	"log"
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

func (h *EdgeFnHandler) List(w http.ResponseWriter, r *http.Request) {
	hookType := r.URL.Query().Get("hookType")
	scripts, err := h.store.List(hookType)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, scripts)
}

func (h *EdgeFnHandler) Create(w http.ResponseWriter, r *http.Request) {
	// Limit request body to MaxCodeSize + overhead for JSON fields
	r.Body = http.MaxBytesReader(w, r.Body, int64(edgefn.MaxCodeSize+4096))

	var script edgefn.Script
	if err := json.NewDecoder(r.Body).Decode(&script); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	script.Active = true

	// Validate (ID format, name, code size, hookType)
	if err := script.Validate(); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	if err := h.store.Save(&script); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}

	// Deploy to Deno runtime — rollback store on failure
	if err := h.client.Deploy(r.Context(), script.ID, script.Code); err != nil {
		if delErr := h.store.Delete(script.ID); delErr != nil {
			log.Printf("WARN: failed to rollback script store: %v", delErr)
		}
		httpError(w, "failed to deploy: "+safeError(err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(script)
}

func (h *EdgeFnHandler) Get(w http.ResponseWriter, r *http.Request) {
	fnId := chi.URLParam(r, "fnId")
	if err := edgefn.ValidateID(fnId); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	script, err := h.store.Get(fnId)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if script == nil {
		httpError(w, "function not found", http.StatusNotFound)
		return
	}
	writeJSON(w, script)
}

func (h *EdgeFnHandler) Delete(w http.ResponseWriter, r *http.Request) {
	fnId := chi.URLParam(r, "fnId")
	if err := edgefn.ValidateID(fnId); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if err := h.client.Delete(r.Context(), fnId); err != nil {
		log.Printf("WARN: failed to delete function from runtime: %v", err)
	}
	if err := h.store.Delete(fnId); err != nil {
		log.Printf("WARN: failed to delete function from store: %v", err)
	}
	writeJSON(w, map[string]string{"status": "deleted", "id": fnId})
}

func (h *EdgeFnHandler) Invoke(w http.ResponseWriter, r *http.Request) {
	fnId := chi.URLParam(r, "fnId")
	if err := edgefn.ValidateID(fnId); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}

	// Limit invoke payload
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024) // 1 MB max

	script, err := h.store.Get(fnId)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if script == nil {
		httpError(w, "function not found", http.StatusNotFound)
		return
	}

	var data interface{}
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	result, err := h.client.Invoke(r.Context(), fnId, data)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
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
