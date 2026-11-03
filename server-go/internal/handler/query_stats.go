package handler

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// QueryStatsHandler serves the slow-queries page and tool (EXC-654). The
// router gates each route by role: reads any member, reset Developer, turning
// statistics on Admin, as it restarts the database.
type QueryStatsHandler struct {
	svc *service.QueryStatsService
}

func NewQueryStatsHandler(svc *service.QueryStatsService) *QueryStatsHandler {
	return &QueryStatsHandler{svc: svc}
}

// List answers ?sort=total|mean&limit=N, and what the caller may do next.
func (h *QueryStatsHandler) List(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httpError(w, "limit must be a number", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	projectID := chi.URLParam(r, "projectId")
	report, err := h.svc.List(r.Context(), projectID, r.URL.Query().Get("sort"), limit)
	if err != nil {
		writeQueryStatsError(w, projectID, "read", err)
		return
	}
	access := custommw.ProjectAccessFromContext(r.Context())
	writeJSON(w, struct {
		*service.SlowQueryReport
		CanReset  bool `json:"canReset"`
		CanEnable bool `json:"canEnable"`
	}{report, access != nil && access.RoleAtLeast(domain.OrgRoleDeveloper), access != nil && access.RoleAtLeast(domain.OrgRoleAdmin)})
}

func (h *QueryStatsHandler) Reset(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := h.svc.Reset(r.Context(), projectID); err != nil {
		writeQueryStatsError(w, projectID, "reset", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Enable answers 202: a Postgres database restarts to load the library.
func (h *QueryStatsHandler) Enable(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	if err := h.svc.Enable(r.Context(), projectID); err != nil {
		writeQueryStatsError(w, projectID, "enable", err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func writeQueryStatsError(w http.ResponseWriter, projectID, what string, err error) {
	status := queryStatsStatus(err)
	if status >= http.StatusInternalServerError {
		log.Printf("ERROR: query statistics %s for project %s: %v", what, projectID, err)
		httpError(w, "query statistics are unavailable; try again later", status)
		return
	}
	httpError(w, safeError(err), status)
}

func queryStatsStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrQueryStatsSort):
		return http.StatusBadRequest
	case errors.Is(err, service.ErrQueryStatsOff), errors.Is(err, service.ErrNotForMySQL),
		errors.Is(err, service.ErrKubernetesOnly), errors.Is(err, service.ErrQueryStatsDocumentDB):
		return http.StatusConflict
	}
	return unsettledOr(err, http.StatusInternalServerError)
}
