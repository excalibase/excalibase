package storage

import (
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ErrOrgProjectLimitReached is returned when registering a project would take
// an organisation past the number of projects its tier allows. It is the one
// answer every store gives, so the service layer can recognise it without
// knowing which store it is talking to.
var ErrOrgProjectLimitReached = errors.New("organisation has reached its project limit")

// HoldsOrgProjectSlot reports whether a project in this status occupies one of
// its organisation's tier project slots.
//
// Every status holds one except the two deletion states. A deleted project
// keeps its row until teardown is observed complete, so counting those states
// would keep the user out of a slot they have already given up — and a
// teardown that is stuck (a namespace that will not finalize, backups that
// cannot be purged) would hold that slot forever.
//
// FAILED holds a slot on purpose: a failed provision leaves a project the user
// can still see, retry or delete, and its resources may well exist. It stops
// counting the moment the user deletes it, which is the moment they decide the
// slot is free.
func HoldsOrgProjectSlot(status string) bool {
	return !domain.IsDeletionStatus(status)
}

// NonSlotStatuses lists the statuses HoldsOrgProjectSlot rejects, for stores
// that count in SQL rather than in Go.
func NonSlotStatuses() []string {
	return []string{string(domain.StatusDeleting), string(domain.StatusBackupsPendingDelete)}
}

// CheckOrgProjectSlot reports whether an organisation already holding `held`
// slots may take one more. maxProjects of zero or less means unlimited.
func CheckOrgProjectSlot(held, maxProjects int) error {
	if maxProjects <= 0 || held < maxProjects {
		return nil
	}
	return fmt.Errorf("%w: %d of %d slots held", ErrOrgProjectLimitReached, held, maxProjects)
}

// CountOrgProjectSlots counts the slot-holding projects of one organisation,
// for the stores that hold every instance in memory keyed by project id.
func CountOrgProjectSlots(instances map[string]*domain.DatabaseInstance, orgID string) int {
	held := 0
	for _, inst := range instances {
		if inst.OrgID == orgID && HoldsOrgProjectSlot(inst.Status) {
			held++
		}
	}
	return held
}

// AdmitOrgProject applies the create rules the in-memory stores share: an id
// another project holds is a conflict, and an organisation with no free slot
// is refused. Callers hold their own lock around it and write only on nil.
func AdmitOrgProject(instances map[string]*domain.DatabaseInstance, inst *domain.DatabaseInstance, maxProjects int) error {
	if _, taken := instances[inst.ProjectID]; taken {
		return ErrProjectExists
	}
	return CheckOrgProjectSlot(CountOrgProjectSlots(instances, inst.OrgID), maxProjects)
}
