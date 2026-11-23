package main

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// newOrgGuard refuses to start compute for an organisation whose owner the
// admin console abuse-suspended (EXC-843). The suspension list is read on use.
func newOrgGuard(instances storage.InstanceStore, sqlStore storage.PlatformStore) *orgguard.Guard {
	return orgguard.New(instances, func(ctx context.Context) (map[string]bool, error) {
		return pgstore.NewAdminConsole(sqlStore.DB()).OwnerSuspendedOrgs(ctx)
	})
}
