package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// --- Query Execution ---

func (h *SchemaHandler) ExecuteDDL(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var body struct {
		SQL string `json:"sql"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	result := h.introspector.ExecuteDDL(r.Context(), db, body.SQL)
	writeJSON(w, result)
}

func (h *SchemaHandler) ExecuteQuery(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if body.Query == "" {
		httpError(w, "query is required", http.StatusBadRequest)
		return
	}
	result := h.introspector.ExecuteQuery(r.Context(), db, body.Query)
	writeJSON(w, result)
}

func (h *SchemaHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	projectId := chi.URLParam(r, "projectId")
	db, err := h.getDB(projectId)
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	connected := h.introspector.TestConnection(r.Context(), db)
	writeJSON(w, map[string]interface{}{
		"connected": connected,
		"projectId": projectId,
	})
}
