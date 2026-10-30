package hostmonitor

import (
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
)

// Attribution names each managed container's project and role by the rule
// the monitor uses; the single-host log shipper labels lines with it (EXC-672).
func Attribution(instances []*domain.DatabaseInstance) func(enginestats.Container) (string, string) {
	return attribution(instances)
}
