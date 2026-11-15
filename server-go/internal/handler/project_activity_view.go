package handler

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
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
}

// SetActivityStore wires the last-seen lookup. Optional: without it the
// endpoints return the bare instance.
func (h *ProvisioningHandler) SetActivityStore(s storage.ProjectActivityStore) { h.activity = s }

// projectResponse decorates one instance with its last-seen marker.
func (h *ProvisioningHandler) projectResponse(ctx context.Context, inst *domain.DatabaseInstance) projectView {
	inst = visibleTo(ctx, inst)
	if h.activity == nil {
		return projectView{DatabaseInstance: inst}
	}
	activity, ok, err := h.activity.GetProjectActivity(ctx, inst.ProjectID)
	if err != nil {
		log.Printf("WARN: project activity lookup failed: %v", err)
	}
	return withActivity(inst, activity, ok)
}

// projectListResponse decorates a list with one activity read, not one per row.
func (h *ProvisioningHandler) projectListResponse(ctx context.Context, insts []*domain.DatabaseInstance) any {
	visible := make([]*domain.DatabaseInstance, len(insts))
	for i, inst := range insts {
		visible[i] = visibleTo(ctx, inst)
	}
	insts = visible
	if h.activity == nil {
		return insts
	}
	byProject, err := h.activity.ListProjectActivity(ctx)
	if err != nil {
		log.Printf("WARN: project activity list failed: %v", err)
	}
	out := make([]projectView, 0, len(insts))
	for _, inst := range insts {
		activity, ok := byProject[inst.ProjectID]
		out = append(out, withActivity(inst, activity, ok))
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
