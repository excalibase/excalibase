package service

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/planbudget"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// SetBudgetReserver wires the store that admits a database resize against the
// plan budget before the cluster changes (EXC-901).
func (s *ProvisioningService) SetBudgetReserver(reserver storage.ProjectBudgetStore) {
	s.reserver = reserver
}

// reserveResize admits and records the size the plan gives the database before
// anything on the cluster moves, and returns the call that puts the old size
// back when the change fails afterwards. A refusal is a *planbudget.Refusal.
func (s *ProvisioningService) reserveResize(ctx context.Context, inst *domain.DatabaseInstance, plan config.TierConfig, disk string) (func(), error) {
	if s.reserver == nil {
		return func() {}, nil
	}
	next, err := resizeReservation(plan, disk)
	if err != nil {
		if s.reserver.BudgetActive(ctx) {
			return nil, err
		}
		return s.leaveUnrecorded(ctx, inst, err)
	}
	previous, err := s.reserver.ReserveDatabaseSize(ctx, inst.ProjectID, next)
	if err != nil {
		return nil, err
	}
	return s.putBack(ctx, inst.ProjectID, previous), nil
}

// resizeReservation is the size the plan gives a database: its per-copy CPU, memory and disk, and its copies.
func resizeReservation(plan config.TierConfig, disk string) (planbudget.Reservation, error) {
	cpu, err := planbudget.ParseCPUMilli(plan.CPU)
	if err != nil {
		return planbudget.Reservation{}, fmt.Errorf("plan cpu: %w", err)
	}
	memory, err := planbudget.ParseBytes(plan.Memory)
	if err != nil {
		return planbudget.Reservation{}, fmt.Errorf("plan memory: %w", err)
	}
	diskBytes, err := planbudget.ParseBytes(disk)
	if err != nil {
		return planbudget.Reservation{}, fmt.Errorf("plan disk: %w", err)
	}
	copies := int64(plan.Instances)
	if copies < 1 {
		copies = 1
	}
	return planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: cpu, MemoryBytes: memory, DiskBytes: diskBytes}, Copies: copies}, nil
}

// leaveUnrecorded is a resize whose plan the budget cannot read, while the
// budget is off: the resize goes ahead, and the database is marked unrecorded
// so the backfill sizes it from the plan the day the budget comes on.
func (s *ProvisioningService) leaveUnrecorded(ctx context.Context, inst *domain.DatabaseInstance, cause error) (func(), error) {
	log.Printf("WARN: plan budget: the size of %s is left unrecorded while the budget is off: %v", inst.ProjectID, cause)
	previous, err := s.reserver.ReserveDatabaseSize(ctx, inst.ProjectID, planbudget.Reservation{})
	if err != nil {
		return nil, err
	}
	return s.putBack(ctx, inst.ProjectID, previous), nil
}

// putBack returns the call that records the previous size again, without admission, when a change fails afterwards.
func (s *ProvisioningService) putBack(ctx context.Context, projectID string, previous planbudget.Reservation) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := s.reserver.RestoreDatabaseSize(ctx, projectID, previous); err != nil {
			log.Printf("ERROR: put back the reserved size of %s: %v", projectID, err)
		}
	}
}
