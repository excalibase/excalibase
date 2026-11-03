package hostlogs

import (
	"regexp"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

var (
	// projectIDPattern is every project id provisioning generates.
	projectIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	// containerNamePattern is the engine's own name rule.
	containerNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	components           = map[string]bool{
		domain.ContainerDatabase: true, domain.ContainerDocumentDB: true, domain.ContainerWatcher: true,
		domain.ContainerApp: true, domain.ContainerOther: true,
	}
	tiers = map[string]bool{"free": true, "standard": true, "enterprise": true}
)

// Labels are a stream's Loki labels. Provisioning sets every one from what it
// created; nothing a container writes becomes a label.
type Labels struct {
	ProjectID string
	Component string
	Container string
	// Tier is the plan, which Loki's retention_stream reads (EXC-659); empty while per-plan retention is off.
	Tier string
}

func (l Labels) valid() bool {
	return projectIDPattern.MatchString(l.ProjectID) && components[l.Component] &&
		containerNamePattern.MatchString(l.Container) && (l.Tier == "" || tiers[l.Tier])
}

func (l Labels) toMap() map[string]string {
	out := map[string]string{"project_id": l.ProjectID, "component": l.Component, "container": l.Container}
	if l.Tier != "" {
		out["tier"] = l.Tier
	}
	return out
}
