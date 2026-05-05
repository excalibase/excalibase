package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// --- Advisors ---

func (h *SchemaHandler) RunPerformanceAdvisor(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	findings, err := h.introspector.RunPerformanceAdvisor(r.Context(), db, schemaParam(r))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, findings)
}

func (h *SchemaHandler) RunSecurityAdvisor(w http.ResponseWriter, r *http.Request) {
	db, err := h.getDB(chi.URLParam(r, "projectId"))
	if err != nil {
		h.handleDBError(w, err)
		return
	}
	findings, err := h.introspector.RunSecurityAdvisor(r.Context(), db, schemaParam(r))
	if err != nil {
		schemaError(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, findings)
}
