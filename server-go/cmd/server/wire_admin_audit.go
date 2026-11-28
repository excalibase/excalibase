package main

import (
	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/go-chi/chi/v5"
)

// wireAdminAudit builds the console's audit log and account page (EXC-856):
// read-only, over the platform database. The disposable-address list is not wired here, as
// the users table has none, so the flag stays unknown.
func wireAdminAudit(sqlStore storage.PlatformStore) *handler.AdminAuditHandler {
	queries := postgres.NewAdminConsole(sqlStore.DB())
	return handler.NewAdminAuditHandler(adminconsole.NewAuditLog(queries), adminconsole.NewUserDetails(queries, nil))
}

// mountAdminAudit mounts /api/admin/audit and /api/admin/accounts. The handler
// answers anyone but a platform admin's session as an unmounted route does.
func mountAdminAudit(admin chi.Router, d *handlerDeps) {
	if d.adminAudit == nil {
		return
	}
	admin.Route("/audit", func(r chi.Router) { d.adminAudit.AuditRoutes(r) })
	admin.Route("/accounts", func(r chi.Router) { d.adminAudit.AccountRoutes(r) })
}
