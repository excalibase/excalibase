package main

import (
	"context"
	"log"

	"github.com/excalibase/provisioning-poc/internal/adminaccess"
	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// buildAdminConsole serves the platform admin console. Running state comes
// from the same app runtime the deploy service uses, and a suspension stops
// apps through the app lifecycle, never by reaching around it.
func buildAdminConsole(cfg config.AppConfig, sqlStore storage.PlatformStore, runtime service.AppRuntime, apps *service.AppDeployService, mail email.Sender) (*handler.AdminConsoleHandler, *adminconsole.SuspensionService) {
	queries := postgres.NewAdminConsole(sqlStore.DB())
	console := adminconsole.NewConsole(queries, apphost.NewPostgresAppStore(sqlStore.DB()), runtime)
	console.SetAudit(sqlStore)
	suspensions := adminconsole.NewSuspensionService(queries, apps, sqlStore)
	suspensions.SetNotifier(suspensionNotifier(cfg, mail), func(ctx context.Context) bool {
		return features.Enabled(ctx, cfg.Features(), features.SuspensionEmails)
	})
	return handler.NewAdminConsoleHandler(console, suspensions), suspensions
}

// adminGate is the admin host's fence. Access assertions are checked only when
// the team and AUD are both configured; a test may supply its own checker.
func adminGate(cfg config.AppConfig, d *handlerDeps) adminaccess.GateConfig {
	if d.adminGateOverride != nil {
		return *d.adminGateOverride
	}
	gate := adminaccess.GateConfig{AdminHost: cfg.AdminHost, Disabled: cfg.AdminConsoleDisabled(), IsInternal: custommw.IsInternalListener}
	switch {
	case d.adminAssertions != nil:
		gate.Verifier = d.adminAssertions
	case cfg.AdminAccessEnabled():
		gate.Verifier = adminaccess.NewVerifier(adminaccess.Config{TeamDomain: cfg.AdminAccessTeamDomain, Audience: cfg.AdminAccessAud})
	}
	return gate
}

// suspensionNotifier mails the suspended account holder through the platform's
// mail sender. Without one the suspension still stands and the flag does nothing.
func suspensionNotifier(cfg config.AppConfig, mail email.Sender) adminconsole.SuspensionNotifier {
	if !email.Configured(mail) {
		log.Printf("suspension mail: no mail sender configured, suspended account holders are not emailed")
		return nil
	}
	return service.NewSuspensionMail(mail, service.SuspensionMailConfig{
		From: cfg.EmailFromAddress, FromName: cfg.EmailFromName, SupportEmail: cfg.SupportEmail, PolicyURL: cfg.AcceptableUseURL,
	})
}
