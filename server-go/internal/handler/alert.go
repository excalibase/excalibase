package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

type AlertHandler struct {
	svc *service.AlertingService
	// instances and orgs resolve which projects the caller may see. The
	// platform-wide reads refuse to answer without them rather than fall
	// back to the unscoped list.
	instances storage.InstanceStore
	orgs      storage.OrgStore
}

func NewAlertHandler(svc *service.AlertingService) *AlertHandler { return &AlertHandler{svc: svc} }

// SetScope wires the stores the platform-wide alert reads scope their results
// with. main.go always wires them; a handler built without them answers 503.
func (h *AlertHandler) SetScope(instances storage.InstanceStore, orgs storage.OrgStore) {
	h.instances, h.orgs = instances, orgs
}

// Routes registers the platform-wide alert reads. The per-project read is
// registered by ProjectRoutes under a {projectId} mount so the project gate
// runs with the param bound (a gate on this level would see no projectId).
func (h *AlertHandler) Routes(r chi.Router) {
	r.Get("/", h.GetActive)
	r.Get("/history", h.GetHistory)
}

// ProjectRoutes registers the alert reads relative to an already-bound
// {projectId}; the mount is responsible for RequireProjectAccess.
func (h *AlertHandler) ProjectRoutes(r chi.Router) {
	r.Get("/", h.GetForProject)
}

func (h *AlertHandler) GetActive(w http.ResponseWriter, r *http.Request) {
	h.writeScoped(w, r, h.svc.GetActiveAlerts())
}

func (h *AlertHandler) GetHistory(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil {
			limit = v
		}
	}
	h.writeScoped(w, r, h.svc.GetAlertHistory(limit))
}

func (h *AlertHandler) GetForProject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	writeJSON(w, h.svc.GetActiveAlertsForProject(projectID))
}

// writeScoped answers with only the alerts whose project the caller may see.
// An alert names a project, so an unscoped list is a cross-tenant inventory of
// which projects exist and how they are failing (EXC-418).
func (h *AlertHandler) writeScoped(w http.ResponseWriter, r *http.Request, alerts []domain.Alert) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "auth required", http.StatusUnauthorized)
		return
	}
	if h.instances == nil || h.orgs == nil {
		httpError(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	visible := h.projectVisibility(r, user)
	out := make([]domain.Alert, 0, len(alerts))
	for _, alert := range alerts {
		if visible(alert.ProjectID) {
			out = append(out, alert)
		}
	}
	writeJSON(w, out)
}

// projectVisibility returns a predicate over project ids, memoised because a
// history page holds many alerts and few distinct projects.
func (h *AlertHandler) projectVisibility(r *http.Request, user *domain.User) func(string) bool {
	ctx := r.Context()
	token := auth.GetToken(ctx)
	seen := map[string]bool{}
	return func(projectID string) bool {
		if allowed, ok := seen[projectID]; ok {
			return allowed
		}
		allowed := h.canSeeProject(ctx, user, token, projectID)
		seen[projectID] = allowed
		return allowed
	}
}

// canSeeProject answers for one project. A platform operator sees every
// tenant, but a credential narrowed to one project never leaves it — the role
// is wide, the credential is not.
func (h *AlertHandler) canSeeProject(ctx context.Context, user *domain.User, token *domain.AccessToken, projectID string) bool {
	if projectID == "" || !auth.TokenBoundToProject(token, projectID) {
		return false
	}
	if auth.HasPermission(user.Role, auth.PermViewAny) {
		return true
	}
	return custommw.ResolveProjectAccess(ctx, user, token, projectID, h.instances, h.orgs) != nil
}
