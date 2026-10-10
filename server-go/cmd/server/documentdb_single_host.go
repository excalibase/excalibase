package main

import (
	"context"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/docbrowser"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// singleHostGateways reads a single host's DocumentDB gateway CA for the
// document browser (EXC-576), from the gateway container itself.
type singleHostGateways struct{ docker provisioner.DockerClient }

func (g singleHostGateways) GatewayCA(ctx context.Context, projectID string) ([]byte, error) {
	state, err := g.docker.ContainerState(ctx, provisioner.DocumentDBGatewayName(projectID))
	if err != nil {
		return nil, err
	}
	if !state.Running {
		return nil, docbrowser.ErrGatewayNotReady
	}
	return provisioner.DocumentDBGatewayCA(ctx, g.docker, projectID)
}

const (
	documentDBGatewayWatchLockID   int64 = 0x6464_6267_7477_0576
	documentDBGatewayWatchInterval       = 30 * time.Second
)

// startDocumentDBGatewayWatch restarts single-host gateways whose database
// restarted under them (EXC-576); Kubernetes restarts its own pods.
func startDocumentDBGatewayWatch(cfg config.AppConfig, sqlStore storage.PlatformStore, provSvc *service.ProvisioningService) func() {
	if !documentDBGatewayWatchWanted(cfg) {
		return func() {
			// no single-host DocumentDB gateways to watch
		}
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() && sqlStore != nil && sqlStore.DB() != nil {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), documentDBGatewayWatchLockID)
	}
	log.Printf("DocumentDB gateway watch started (every %s)", documentDBGatewayWatchInterval)
	return provSvc.StartDocumentDBGatewayWatch(context.Background(), service.NewLeadership(lock), documentDBGatewayWatchInterval)
}

func documentDBGatewayWatchWanted(cfg config.AppConfig) bool {
	return cfg.ProvisionerMode == "docker" && cfg.DocumentDBEnabled
}

// buildSingleHostEndpoints describes a single host's projects as reached on
// its loopback (EXC-576), or is nil off a single host.
func buildSingleHostEndpoints(cfg config.AppConfig, store storage.InstanceStore, docker provisioner.DockerClient) *service.SingleHostEndpointService {
	if cfg.ProvisionerMode != "docker" || docker == nil {
		return nil
	}
	return service.NewSingleHostEndpointService(store, docker)
}
