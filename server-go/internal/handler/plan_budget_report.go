package handler

import (
	"context"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

// PlanBudgetSource reads an org's plan budget for display.
type PlanBudgetSource interface {
	PlanBudgetReport(ctx context.Context, orgID string) (planbudget.Report, error)
}

// PlanBudgetHandler shows an org's members what their plan allows and what is
// left. Its route is mounted behind the planbudget feature gate.
type PlanBudgetHandler struct {
	source  PlanBudgetSource
	members creditMembers
}

func NewPlanBudgetHandler(source PlanBudgetSource, members creditMembers) *PlanBudgetHandler {
	return &PlanBudgetHandler{source: source, members: members}
}

// OrgRoutes mounts the read under /api/orgs/{orgId}.
func (h *PlanBudgetHandler) OrgRoutes(r chi.Router, gate func(http.Handler) http.Handler) {
	r.With(gate).Get("/plan-budget", h.Report)
}

func (h *PlanBudgetHandler) Report(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	orgID := chi.URLParam(r, "orgId")
	if user == nil {
		httpError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	if !auth.IsPlatformServicePrincipal(user, auth.GetToken(r.Context())) {
		if member, err := h.members.GetOrgMember(r.Context(), orgID, user.ID); err != nil || member == nil {
			httpError(w, errOrgNotFound, http.StatusNotFound)
			return
		}
	}
	report, err := h.source.PlanBudgetReport(r.Context(), orgID)
	if err != nil {
		log.Printf("ERROR: read plan budget of %s: %v", orgID, err)
		httpError(w, "could not read the plan budget", http.StatusInternalServerError)
		return
	}
	writeJSON(w, report)
}
