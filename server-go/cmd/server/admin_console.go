package main

import (
	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// buildAdminConsole serves the platform admin console. Running state comes
// from the same app runtime the deploy service uses, and a suspension stops
// apps through the app lifecycle, never by reaching around it.
func buildAdminConsole(sqlStore storage.PlatformStore, runtime service.AppRuntime, apps *service.AppDeployService) *handler.AdminConsoleHandler {
	queries := postgres.NewAdminConsole(sqlStore.DB())
	console := adminconsole.NewConsole(queries, apphost.NewPostgresAppStore(sqlStore.DB()), runtime)
	suspensions := adminconsole.NewSuspensionService(queries, apps, sqlStore)
	return handler.NewAdminConsoleHandler(console, suspensions)
}
