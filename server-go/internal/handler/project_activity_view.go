package handler

import (
	"context"
	"log"

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
}

// SetActivityStore wires the last-seen lookup. Optional: without it the
// endpoints return the bare instance.
func (h *ProvisioningHandler) SetActivityStore(s storage.ProjectActivityStore) { h.activity = s }

// projectResponse decorates one instance with its last-seen marker.
func (h *ProvisioningHandler) projectResponse(ctx context.Context, inst *domain.DatabaseInstance) any {
	if h.activity == nil {
		return inst
	}
	activity, ok, err := h.activity.GetProjectActivity(ctx, inst.ProjectID)
	if err != nil {
		log.Printf("WARN: project activity lookup failed: %v", err)
	}
	return withLastSeen(inst, activity, ok)
}

// projectListResponse decorates a list with one activity read, not one per row.
func (h *ProvisioningHandler) projectListResponse(ctx context.Context, insts []*domain.DatabaseInstance) any {
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
		out = append(out, withLastSeen(inst, activity, ok))
	}
	return out
}

func withLastSeen(inst *domain.DatabaseInstance, activity domain.ProjectActivity, seen bool) projectView {
	view := projectView{DatabaseInstance: inst}
	if seen {
		view.LastSeenAt = &domain.FlexTime{Time: activity.LastSeenAt}
	}
	return view
}
