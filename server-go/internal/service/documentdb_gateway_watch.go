package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A single host's DocumentDB gateway shares its database container's network
// namespace (EXC-576). An engine that restarts the database, on a crash or a
// host reboot, gives it a new namespace while the gateway holds the old one,
// so a watch restarts every gateway that started before its database.

// OperationGatewayRestart holds the project lease so a pause or a deletion
// never races the restart.
const OperationGatewayRestart ProjectOperation = "DocumentDB gateway restart"

// ErrNoContainerEngine is a single-host operation on a platform without one.
var ErrNoContainerEngine = errors.New("the platform has no container engine")

// RefreshDocumentDBGateways restarts the stale gateways of active single-host
// DocumentDB projects, skipping a project another operation holds. It returns
// every failure, after trying every project.
func (s *ProvisioningService) RefreshDocumentDBGateways(ctx context.Context) error {
	if s.dockerClient == nil {
		return ErrNoContainerEngine
	}
	projects, err := s.store.FindAll()
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	var failures []error
	for _, inst := range projects {
		if !inst.DocumentDB || inst.DeploymentMode != domain.ModeDocker || inst.Status != string(domain.StatusActive) {
			continue
		}
		if err := s.refreshDocumentDBGateway(ctx, inst.ProjectID); err != nil && !errors.Is(err, ErrProjectOperationRunning) {
			failures = append(failures, fmt.Errorf("project %s: %w", inst.ProjectID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *ProvisioningService) refreshDocumentDBGateway(ctx context.Context, projectID string) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationGatewayRestart, requireActive)
	if err != nil {
		return err
	}
	defer release()
	stale, err := provisioner.DocumentDBGatewayStale(ctx, s.dockerClient, projectID, inst.Namespace)
	if err != nil || !stale {
		return err
	}
	log.Printf("INFO: DocumentDB gateway of %s outlived its database's network namespace; restarting it", projectID)
	return provisioner.RestartDocumentDBGateway(ctx, s.dockerClient, projectID)
}

// StartDocumentDBGatewayWatch runs RefreshDocumentDBGateways now and on every
// tick while this replica leads, until the returned cancel is called.
func (s *ProvisioningService) StartDocumentDBGatewayWatch(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			s.refreshGatewaysIfLeader(ctx, leader)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel
}

func (s *ProvisioningService) refreshGatewaysIfLeader(ctx context.Context, leader LeaderChecker) {
	leads, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("ERROR: DocumentDB gateway watch: leadership check: %v", err)
		return
	}
	if !leads {
		return
	}
	if err := s.RefreshDocumentDBGateways(ctx); err != nil {
		log.Printf("ERROR: DocumentDB gateway watch: %v", err)
	}
}
