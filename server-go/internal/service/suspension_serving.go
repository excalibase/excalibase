package service

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// The steps a suspension takes beyond pausing apps (EXC-845). Each is the
// platform's own teardown or lifecycle call for that surface, so a restore is
// the matching call and nothing needs recording.

// SuspendedSites takes a suspended account's sites off their hostnames.
type SuspendedSites struct{ router ProjectSiteRouter }

func NewSuspendedSites(router ProjectSiteRouter) *SuspendedSites {
	return &SuspendedSites{router: router}
}

func (*SuspendedSites) Name() string { return "sites" }

func (s *SuspendedSites) Stop(ctx context.Context, projectID string) error {
	return s.router.UnrouteProjectSites(ctx, projectID)
}

func (s *SuspendedSites) Restore(ctx context.Context, projectID string) error {
	return s.router.RouteProjectSites(ctx, projectID)
}

// SuspendedWorkloads stops what the project runs besides its database: every
// app route and the function runtime, so functions and cron stop with it.
type SuspendedWorkloads struct{ workloads ProjectWorkloadStopper }

func NewSuspendedWorkloads(workloads ProjectWorkloadStopper) *SuspendedWorkloads {
	return &SuspendedWorkloads{workloads: workloads}
}

func (*SuspendedWorkloads) Name() string { return "function runtime" }

func (s *SuspendedWorkloads) Stop(ctx context.Context, projectID string) error {
	return s.workloads.StopProjectWorkloads(ctx, projectID)
}

// Restore starts the runtime again. The apps come back through their own resume.
func (s *SuspendedWorkloads) Restore(ctx context.Context, projectID string) error {
	return s.workloads.RestartFunctionRuntime(ctx, projectID)
}

// SuspendedEndpoints closes the project's public database port and read
// address, keeping the setting and the port, as a pause does; the unsuspend
// publishes them again.
type SuspendedEndpoints struct {
	instances storage.InstanceStore
	endpoints PublicEndpointReconciler
}

func NewSuspendedEndpoints(instances storage.InstanceStore, endpoints PublicEndpointReconciler) *SuspendedEndpoints {
	return &SuspendedEndpoints{instances: instances, endpoints: endpoints}
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
	inst, err := s.project(projectID)
	if err != nil || inst == nil || inst.Status != string(domain.StatusActive) {
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
