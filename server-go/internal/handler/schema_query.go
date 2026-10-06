package handler

import (
	"encoding/json"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/schema"
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

// ExecuteReadOnlyQuery is GET /query: one statement in a read-only
// transaction, so a read-only token can query without being able to write.
// A sign-in session is refused: Studio runs SQL with POST, and a GET that a
// same-site page could trigger with the cookie must not run SQL at all.
func (h *SchemaHandler) ExecuteReadOnlyQuery(w http.ResponseWriter, r *http.Request) {
	if auth.IsSessionToken(auth.GetToken(r.Context())) {
		httpError(w, "read-only SQL takes a personal access token", http.StatusForbidden)
		return
	}
	query := r.URL.Query().Get("sql")
	if query == "" {
		httpError(w, "sql is required", http.StatusBadRequest)
		return
	}
	if len(query) > schema.MaxReadOnlySQL {
		httpError(w, "sql is too long", http.StatusRequestURITooLong)
		return
	}
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	writeJSON(w, h.introspector.ExecuteReadOnlyQuery(r.Context(), db, query))
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
