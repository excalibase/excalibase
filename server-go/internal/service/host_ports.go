package service

import (
	"context"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// HostPortAllocator gives a single host's project one host port for its
// life (EXC-592) from the public-endpoint allocator: unique, kept through
// restarts and rebuilds, and quarantined when the project goes, so a client
// still dialling it never reaches a stranger's database.
type HostPortAllocator struct {
	store      storage.DatabaseEndpointStore
	window     domain.PortRange
	quarantine time.Duration
}

func NewHostPortAllocator(store storage.DatabaseEndpointStore, window domain.PortRange, quarantine time.Duration) *HostPortAllocator {
	return &HostPortAllocator{store: store, window: window, quarantine: quarantine}
}

// Allocate returns the project's port, taking one the first time.
func (a *HostPortAllocator) Allocate(ctx context.Context, projectID string) (int, error) {
	return a.allocate(ctx, projectID, domain.DBEndpointRolePostgres)
}

func (a *HostPortAllocator) allocate(ctx context.Context, projectID string, role domain.DBEndpointRole) (int, error) {
	endpoint, err := a.store.AllocateDatabaseEndpointPort(ctx, projectID, role, a.window, a.quarantine)
	if err != nil {
		return 0, err
	}
	if endpoint.Port <= 0 {
		return 0, fmt.Errorf("no host port was allocated for %s", projectID)
	}
	return endpoint.Port, nil
}

// AllocateMongo returns a DocumentDB project's gateway port (EXC-594).
func (a *HostPortAllocator) AllocateMongo(ctx context.Context, projectID string) (int, error) {
	return a.allocate(ctx, projectID, domain.DBEndpointRoleMongo)
}

// Release quarantines every port the project holds and drops its record.
func (a *HostPortAllocator) Release(ctx context.Context, projectID string) error {
	for _, role := range []domain.DBEndpointRole{domain.DBEndpointRolePostgres, domain.DBEndpointRoleMongo} {
		if err := a.store.ReleaseDatabaseEndpointPort(ctx, projectID, role, time.Now()); err != nil {
			return err
		}
	}
	return a.store.DeleteDatabaseEndpoint(ctx, projectID)
}
