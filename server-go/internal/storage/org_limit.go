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
// Every status holds one except deletion. A project in its 7-day grace period
// (PENDING_DELETION) gave its slot up when it was deleted, so the org can
// create its replacement at once; restoring it takes a slot again (see
// AdmitOrgProjectUpdate). The teardown states keep their row until teardown is
// observed complete, and a stuck teardown must not hold the slot forever.
//
// FAILED holds a slot on purpose: a failed provision leaves a project the user
// can still see, retry or delete, and its resources may well exist. It stops
// counting the moment the user deletes it, which is the moment they decide the
// slot is free.
func HoldsOrgProjectSlot(status string) bool {
	return status != string(domain.StatusPendingDeletion) && !domain.IsDeletionStatus(status)
}

// NonSlotStatuses lists the statuses HoldsOrgProjectSlot rejects, for stores
// that count in SQL rather than in Go.
func NonSlotStatuses() []string {
	return []string{
		string(domain.StatusPendingDeletion),
		string(domain.StatusDeleting),
		string(domain.StatusBackupsPendingDelete),
	}
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

// AdmitOrgProjectUpdate applies the UpdateIfStatusWithinOrgLimit rules the
// in-memory stores share: the row must exist, not be under teardown and still
// hold expected, and a write that moves it back into a slot-holding status
// needs a free slot. Callers hold their own lock around it and write only on nil.
func AdmitOrgProjectUpdate(instances map[string]*domain.DatabaseInstance, inst *domain.DatabaseInstance, expected string, maxProjects int) error {
	stored, ok := instances[inst.ProjectID]
	if !ok {
		return ErrProjectNotFound
	}
	if err := CheckUpdatable(stored); err != nil {
		return err
	}
	if stored.Status != expected {
		return fmt.Errorf("%w: %s is %s, expected %s", ErrProjectStatusChanged, inst.ProjectID, stored.Status, expected)
	}
	if !HoldsOrgProjectSlot(inst.Status) || HoldsOrgProjectSlot(stored.Status) {
		return nil
	}
	return CheckOrgProjectSlot(CountOrgProjectSlots(instances, stored.OrgID), maxProjects)
}
