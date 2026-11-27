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
	cpu, err := planbudget.ParseCPUMilli(plan.CPU)
	if err != nil {
		return nil, fmt.Errorf("plan cpu: %w", err)
	}
	memory, err := planbudget.ParseBytes(plan.Memory)
	if err != nil {
		return nil, fmt.Errorf("plan memory: %w", err)
	}
	diskBytes, err := planbudget.ParseBytes(disk)
	if err != nil {
		return nil, fmt.Errorf("plan disk: %w", err)
	}
	copies := int64(plan.Instances)
	if copies < 1 {
		copies = 1
	}
	next := planbudget.Reservation{PerCopy: planbudget.Size{CPUMilli: cpu, MemoryBytes: memory, DiskBytes: diskBytes}, Copies: copies}
	previous, err := s.reserver.ReserveDatabaseSize(ctx, inst.ProjectID, next)
	if err != nil {
		return nil, err
	}
	return func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := s.reserver.RestoreDatabaseSize(ctx, inst.ProjectID, previous); err != nil {
			log.Printf("ERROR: put back the reserved size of %s: %v", inst.ProjectID, err)
		}
	}, nil
}
