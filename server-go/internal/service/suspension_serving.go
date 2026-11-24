package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// The steps a suspension takes beyond pausing apps (EXC-845). Each is the
// platform's own teardown or lifecycle call for that surface, so a restore is
// the matching call and nothing needs recording.

// restoreGate decides, for each project an unsuspend would bring back, whether
// it may be served now. The admin's override lifts the guard for the restore
// itself, so the standing is read afresh and without it: a project awaiting
// deletion, in teardown or restore, or whose org owner is still suspended
// stays down.
type restoreGate struct {
	instances storage.InstanceStore
	guard     *orgguard.Guard
}

// servable is false, with no error, for a project that must stay down.
func (g restoreGate) servable(ctx context.Context, projectID string) (*domain.DatabaseInstance, bool, error) {
	inst, err := g.instances.FindByProjectID(projectID)
	if err != nil {
		return nil, false, fmt.Errorf("read the project: %w", err)
	}
	if inst == nil || domain.IsNotServable(inst.Status) {
		return nil, false, nil
	}
	err = g.guard.CheckServingFresh(ctx, projectID)
	switch {
	case err == nil:
		return inst, true, nil
	case errors.Is(err, orgguard.ErrOrgSuspended):
		return nil, false, nil
	default:
		return nil, false, err
	}
}

// SuspendedSites takes a suspended account's sites off their hostnames.
type SuspendedSites struct {
	router ProjectSiteRouter
	gate   restoreGate
}

func NewSuspendedSites(router ProjectSiteRouter, instances storage.InstanceStore, guard *orgguard.Guard) *SuspendedSites {
	return &SuspendedSites{router: router, gate: restoreGate{instances, guard}}
}

func (*SuspendedSites) Name() string { return "sites" }

func (s *SuspendedSites) Stop(ctx context.Context, projectID string) error {
	return s.router.UnrouteProjectSites(ctx, projectID)
}

func (s *SuspendedSites) Restore(ctx context.Context, projectID string) error {
	if _, ok, err := s.gate.servable(ctx, projectID); err != nil || !ok {
		return err
	}
	return s.router.RouteProjectSites(ctx, projectID)
}

// SuspendedWorkloads stops what the project runs besides its database: every
// app route and the function runtime, so functions and cron stop with it.
type SuspendedWorkloads struct {
	workloads ProjectWorkloadStopper
	gate      restoreGate
}

func NewSuspendedWorkloads(workloads ProjectWorkloadStopper, instances storage.InstanceStore, guard *orgguard.Guard) *SuspendedWorkloads {
	return &SuspendedWorkloads{workloads: workloads, gate: restoreGate{instances, guard}}
}

func (*SuspendedWorkloads) Name() string { return "function runtime" }

func (s *SuspendedWorkloads) Stop(ctx context.Context, projectID string) error {
	return s.workloads.StopProjectWorkloads(ctx, projectID)
}

// Restore starts the runtime again. The apps come back through their own resume.
func (s *SuspendedWorkloads) Restore(ctx context.Context, projectID string) error {
	if _, ok, err := s.gate.servable(ctx, projectID); err != nil || !ok {
		return err
	}
	return s.workloads.RestartFunctionRuntime(ctx, projectID)
}

// SuspendedEndpoints closes the project's public database port and read
// address, keeping the setting and the port, as a pause does; the unsuspend
// publishes them again.
type SuspendedEndpoints struct {
	instances storage.InstanceStore
	endpoints PublicEndpointReconciler
	gate      restoreGate
}

func NewSuspendedEndpoints(instances storage.InstanceStore, endpoints PublicEndpointReconciler, guard *orgguard.Guard) *SuspendedEndpoints {
	return &SuspendedEndpoints{instances: instances, endpoints: endpoints, gate: restoreGate{instances, guard}}
}

func (*SuspendedEndpoints) Name() string { return "public database port" }

func (s *SuspendedEndpoints) Stop(ctx context.Context, projectID string) error {
	inst, err := s.project(projectID)
	if err != nil || inst == nil {
		return err
	}
	return s.endpoints.Withdraw(ctx, inst)
}

// Restore publishes only what a running database serves; a paused project's
// port comes back with its resume.
func (s *SuspendedEndpoints) Restore(ctx context.Context, projectID string) error {
	inst, ok, err := s.gate.servable(ctx, projectID)
	if err != nil || !ok || inst.Status != string(domain.StatusActive) {
		return err
	}
	return s.endpoints.Publish(ctx, inst)
}

func (s *SuspendedEndpoints) project(projectID string) (*domain.DatabaseInstance, error) {
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return nil, fmt.Errorf("read the project: %w", err)
	}
	return inst, nil
}
