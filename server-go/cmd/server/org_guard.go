package main

import (
	"context"
	"net/http"

	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// newOrgGuard refuses to start compute for an organisation whose owner the
// admin console abuse-suspended (EXC-843). The suspension list is read on use.
func newOrgGuard(instances storage.InstanceStore, sqlStore storage.PlatformStore) *orgguard.Guard {
	return orgguard.New(instances, func(ctx context.Context) (map[string]bool, error) {
		return pgstore.NewAdminConsole(sqlStore.DB()).OwnerSuspendedOrgs(ctx)
	}).WithSuspendedUsers(func(ctx context.Context) (map[string]bool, error) {
		return pgstore.NewAdminConsole(sqlStore.DB()).SuspendedUsers(ctx)
	})
}

// projectAccess binds the path project to the caller, then refuses a write on
// the project of a suspended owner's org (EXC-845) and a container write on
// a limited owner's project (EXC-899).
func projectAccess(store storage.InstanceStore, orgs storage.OrgStore, d *handlerDeps) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		custommw.RequireProjectAccess(store, orgs),
		custommw.RefuseSuspendedWrites(d.orgGuard),
		custommw.RequireVerifiedForContainers(d.trustGate),
	}
}
