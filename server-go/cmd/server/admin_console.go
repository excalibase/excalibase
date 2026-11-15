package main

import (
	"github.com/excalibase/provisioning-poc/internal/adminaccess"
	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// buildAdminConsole serves the platform admin console. Running state comes
// from the same app runtime the deploy service uses, and a suspension stops
// apps through the app lifecycle, never by reaching around it.
func buildAdminConsole(sqlStore storage.PlatformStore, runtime service.AppRuntime, apps *service.AppDeployService) (*handler.AdminConsoleHandler, *adminconsole.SuspensionService) {
	queries := postgres.NewAdminConsole(sqlStore.DB())
	console := adminconsole.NewConsole(queries, apphost.NewPostgresAppStore(sqlStore.DB()), runtime)
	console.SetAudit(sqlStore)
	suspensions := adminconsole.NewSuspensionService(queries, apps, sqlStore)
	return handler.NewAdminConsoleHandler(console, suspensions), suspensions
}

// adminGate is the admin host's fence. Access assertions are checked only when
// the team and AUD are both configured; a test may supply its own checker.
func adminGate(cfg config.AppConfig, d *handlerDeps) adminaccess.GateConfig {
	gate := adminaccess.GateConfig{AdminHost: cfg.AdminHost, IsInternal: custommw.IsInternalListener}
	switch {
	case d.adminAssertions != nil:
		gate.Verifier = d.adminAssertions
	case cfg.AdminAccessEnabled():
		gate.Verifier = adminaccess.NewVerifier(adminaccess.Config{TeamDomain: cfg.AdminAccessTeamDomain, Audience: cfg.AdminAccessAud})
	}
	return gate
}
