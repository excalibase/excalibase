package main

import (
	"context"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// mountDBReadAddress serves a database's read-only address (EXC-666): reads
// Developer+, opening its public port Admin+; unmounted while the flag is off.
func mountDBReadAddress(r *chi.Mux, sqlStore storage.OrgStore, store storage.InstanceStore, d *handlerDeps) {
	readAddress := d.dbReadAddressHandler
	if readAddress == nil {
		readAddress = handler.NewDBReadAddressHandler(nil, nil)
	}
	r.Route("/api/projects/{projectId}/db-read-address", func(r chi.Router) {
		r.Use(features.Require(d.features, features.DBReadAddress))
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore))
		r.Use(custommw.RequireProjectRoleForWrites(domain.OrgRoleAdmin, store, sqlStore))
		r.Use(custommw.RequireProjectDatabase(store))
		r.Use(d.activity)
		readAddress.Routes(r)
	})
}

// newDBReadAddressHandler starts without its service, which is wired once built.
func newDBReadAddressHandler(sqlStore any) *handler.DBReadAddressHandler {
	audit, _ := sqlStore.(interface {
		LogAudit(ctx context.Context, entry *domain.AuditEntry) error
	})
	return handler.NewDBReadAddressHandler(nil, audit)
}
