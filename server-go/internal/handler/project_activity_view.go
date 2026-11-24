package handler

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// projectView is the project payload the GET / list endpoints return: the
// stored instance plus the last-seen marker from project_activity. The
// marker is joined at read time rather than persisted on the instance so the
// throttled activity writes never touch database_instances.
type projectView struct {
	*domain.DatabaseInstance
	LastSeenAt *domain.FlexTime `json:"lastSeenAt,omitempty"`
	// IdleWarnedAt is set while an idle-pause warning is outstanding.
	IdleWarnedAt *domain.FlexTime `json:"idleWarnedAt,omitempty"`
	// CanAddDatabase says whether this caller may add a database to the
	// project now (EXC-426). The Studio shows the control only on true; the
	// route's own Admin rule still decides.
	CanAddDatabase bool `json:"canAddDatabase"`
	// OwnerSuspended is set while the project's org owner is suspended, so the
	// Studio shows "Suspended" instead of the database's own status (EXC-757).
	OwnerSuspended bool `json:"ownerSuspended,omitempty"`
}

// SuspendedOrgsLookup names the orgs whose owner is suspended.
type SuspendedOrgsLookup func(ctx context.Context) (map[string]bool, error)

// SetSuspendedOrgs wires the suspended-owner lookup. Optional.
func (h *ProvisioningHandler) SetSuspendedOrgs(lookup SuspendedOrgsLookup) { h.suspendedOrgs = lookup }

// suspendedOrgSet reads the set once per response; a failure is logged and the
// projects stay unmarked rather than failing the listing.
func (h *ProvisioningHandler) suspendedOrgSet(ctx context.Context) map[string]bool {
	if h.suspendedOrgs == nil {
		return nil
	}
	orgs, err := h.suspendedOrgs(ctx)
	if err != nil {
		log.Printf("WARN: suspended owners lookup failed: %v", err)
		return nil
	}
	return orgs
}

// SetActivityStore wires the last-seen lookup. Optional: without it the
// endpoints return the bare instance.
func (h *ProvisioningHandler) SetActivityStore(s storage.ProjectActivityStore) { h.activity = s }

// projectResponse decorates one instance with its last-seen marker.
func (h *ProvisioningHandler) projectResponse(ctx context.Context, inst *domain.DatabaseInstance) projectView {
	inst = visibleTo(ctx, inst)
	view := projectView{DatabaseInstance: inst}
	if h.activity != nil {
		activity, ok, err := h.activity.GetProjectActivity(ctx, inst.ProjectID)
		if err != nil {
			log.Printf("WARN: project activity lookup failed: %v", err)
		}
		view = withActivity(inst, activity, ok)
	}
	view.OwnerSuspended = h.suspendedOrgSet(ctx)[inst.OrgID]
	return view
}

// projectListResponse decorates a list with one activity read, not one per row.
func (h *ProvisioningHandler) projectListResponse(ctx context.Context, insts []*domain.DatabaseInstance) any {
	visible := make([]*domain.DatabaseInstance, len(insts))
	for i, inst := range insts {
		visible[i] = visibleTo(ctx, inst)
	}
	insts = visible
	if h.activity == nil && h.suspendedOrgs == nil {
		return insts
	}
	var byProject map[string]domain.ProjectActivity
	if h.activity != nil {
		var err error
		if byProject, err = h.activity.ListProjectActivity(ctx); err != nil {
			log.Printf("WARN: project activity list failed: %v", err)
		}
	}
	suspended := h.suspendedOrgSet(ctx)
	out := make([]projectView, 0, len(insts))
	for _, inst := range insts {
		activity, ok := byProject[inst.ProjectID]
		view := withActivity(inst, activity, ok)
		view.OwnerSuspended = suspended[inst.OrgID]
		out = append(out, view)
	}
	return out
}

func withActivity(inst *domain.DatabaseInstance, activity domain.ProjectActivity, seen bool) projectView {
	view := projectView{DatabaseInstance: inst}
	if !seen {
		return view
	}
	view.LastSeenAt = &domain.FlexTime{Time: activity.LastSeenAt}
	if activity.IdleWarnedAt != nil {
		view.IdleWarnedAt = &domain.FlexTime{Time: *activity.IdleWarnedAt}
	}
	return view
}

// visibleTo is the project as the caller may see it. The cluster's internal
// addresses and namespace are for platform admins and the console; a customer
// reaches the database through its public endpoint (EXC-759).
func visibleTo(ctx context.Context, inst *domain.DatabaseInstance) *domain.DatabaseInstance {
	if user := auth.GetUser(ctx); user != nil && auth.HasPermission(user.Role, auth.PermManageUsers) {
		return inst
	}
	customer := *inst
	customer.FailureReason = domain.ScrubClusterText(inst.FailureReason, inst.Namespace)
	customer.FailureStep = domain.ScrubClusterText(inst.FailureStep, inst.Namespace)
	customer.RollbackLog = domain.ScrubClusterText(inst.RollbackLog, inst.Namespace)
	customer.DeletionError = domain.ScrubClusterText(inst.DeletionError, inst.Namespace)
	customer.Host, customer.ReadOnlyHost, customer.Namespace = "", "", ""
	customer.PoolerHost, customer.MetricsEndpoint = "", ""
	return &customer
}

// SetServingGuard makes /info refuse a project whose owner is suspended (EXC-845).
func (h *ProvisioningHandler) SetServingGuard(guard *orgguard.Guard) { h.servingGuard = guard }

// refuseSuspendedProject answers 404 for a project the platform must stop
// serving because its owner is suspended, and 503 when that could not be
// checked, so a caller keeps its last good copy rather than a wrong one.
func (h *ProvisioningHandler) refuseSuspendedProject(w http.ResponseWriter, r *http.Request, projectID string) bool {
	err := h.servingGuard.CheckServing(r.Context(), projectID)
	switch {
	case err == nil:
		return false
	case errors.Is(err, orgguard.ErrOrgSuspended):
		httpError(w, "project not found", http.StatusNotFound)
	default:
		log.Printf("ERROR: suspension check for /info of %s: %v", projectID, err)
		httpError(w, "project standing unavailable", http.StatusServiceUnavailable)
	}
	return true
}
