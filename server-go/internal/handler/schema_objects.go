package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/go-chi/chi/v5"
)

const (
	mimeJSON       = "application/json"
	hdrContentType = "Content-Type"
)

// --- Roles ---

func (h *SchemaHandler) GetRoles(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	roles, err := h.introspector.GetRoles(r.Context(), db)
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, roles)
}

func (h *SchemaHandler) CreateRole(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.CreateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		httpError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := h.introspector.CreateRole(r.Context(), db, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set(hdrContentType, mimeJSON)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (h *SchemaHandler) DropRole(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	roleName := chi.URLParam(r, "roleName")
	if err := h.introspector.DropRole(r.Context(), db, roleName); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "dropped"})
}

// --- Extensions ---

func (h *SchemaHandler) GetExtensions(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	exts, err := h.introspector.GetExtensions(r.Context(), db)
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, exts)
}

func (h *SchemaHandler) CreateExtension(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var body struct {
		Name   string `json:"name"`
		Schema string `json:"schema"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		httpError(w, "name is required", http.StatusBadRequest)
		return
	}
	if !schema.IsExtensionAllowed(body.Name) {
		httpError(w, "extension not permitted", http.StatusBadRequest)
		return
	}
	if err := h.introspector.CreateExtension(r.Context(), db, body.Name, body.Schema); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set(hdrContentType, mimeJSON)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (h *SchemaHandler) DropExtension(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	extName := chi.URLParam(r, "extName")
	cascade := r.URL.Query().Get("cascade") == "true"
	if err := h.introspector.DropExtension(r.Context(), db, extName, cascade); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "dropped"})
}

// --- Policies ---

func (h *SchemaHandler) GetPolicies(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	policies, err := h.introspector.GetPolicies(r.Context(), db, schemaParam(r))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, policies)
}

func (h *SchemaHandler) CreatePolicy(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.CreatePolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Table == "" {
		httpError(w, "name and table are required", http.StatusBadRequest)
		return
	}
	if err := h.introspector.CreatePolicy(r.Context(), db, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set(hdrContentType, mimeJSON)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (h *SchemaHandler) DropPolicy(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	policyName := chi.URLParam(r, "policyName")
	tableName := r.URL.Query().Get("table")
	if tableName == "" {
		httpError(w, "table query parameter is required", http.StatusBadRequest)
		return
	}
	if err := h.introspector.DropPolicy(r.Context(), db, tableName, policyName); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "dropped"})
}

// --- Functions ---

func (h *SchemaHandler) GetFunctions(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	funcs, err := h.introspector.GetFunctions(r.Context(), db, schemaParam(r))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, funcs)
}

func (h *SchemaHandler) CreateFunction(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.CreateFunctionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Body == "" {
		httpError(w, "name and body are required", http.StatusBadRequest)
		return
	}
	if err := h.introspector.CreateFunction(r.Context(), db, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set(hdrContentType, mimeJSON)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (h *SchemaHandler) DropFunction(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	funcName := chi.URLParam(r, "funcName")
	argTypes := r.URL.Query().Get("argTypes")
	if err := h.introspector.DropFunction(r.Context(), db, schemaParam(r), funcName, argTypes); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "dropped"})
}

// --- Triggers ---

func (h *SchemaHandler) GetTriggers(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	triggers, err := h.introspector.GetTriggers(r.Context(), db, schemaParam(r))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, triggers)
}

func (h *SchemaHandler) CreateTrigger(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.CreateTriggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Table == "" || req.Function == "" {
		httpError(w, "name, table, and function are required", http.StatusBadRequest)
		return
	}
	if err := h.introspector.CreateTrigger(r.Context(), db, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set(hdrContentType, mimeJSON)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (h *SchemaHandler) DropTrigger(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	triggerName := chi.URLParam(r, "triggerName")
	tableName := r.URL.Query().Get("table")
	if tableName == "" {
		httpError(w, "table query parameter is required", http.StatusBadRequest)
		return
	}
	if err := h.introspector.DropTrigger(r.Context(), db, schemaParam(r), tableName, triggerName); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "dropped"})
}

// --- Indexes (create/drop) ---

func (h *SchemaHandler) CreateIndex(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var req schema.CreateIndexRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Table == "" || len(req.Columns) == 0 {
		httpError(w, "name, table, and columns are required", http.StatusBadRequest)
		return
	}
	if err := h.introspector.CreateIndex(r.Context(), db, req); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set(hdrContentType, mimeJSON)
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

func (h *SchemaHandler) DropIndex(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	indexName := chi.URLParam(r, "indexName")
	if err := h.introspector.DropIndex(r.Context(), db, schemaParam(r), indexName); err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "dropped"})
}

// --- Types ---

func (h *SchemaHandler) GetTypes(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	types, err := h.introspector.GetTypes(r.Context(), db, schemaParam(r))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, types)
}
