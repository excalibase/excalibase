package main

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// selfHealOn is the EXC-873 switch: off, nothing here changes and only the
// admin purge route and auto-pause's own sweep retry a failed operation.
func selfHealOn(_ config.AppConfig, flags features.Flags) bool {
	return features.Enabled(context.Background(), flags, features.SelfHeal)
}

// newRetryEscalator alerts the platform admins, through the alert path the
// backup-credential alert uses, once a retry has failed too often. Nil while
// self-heal is off or there is no alert service.
func newRetryEscalator(cfg config.AppConfig, flags features.Flags, alerts *service.AlertingService, notifier service.AlertNotifier) *service.RetryEscalator {
	if !selfHealOn(cfg, flags) {
		return nil
	}
	return service.NewRetryEscalator(alerts, notifier, cfg.SelfHealAlertAfter)
}

// idleSweepModes says whether the sweep runs and whether it judges idleness.
// With auto-pause off it still runs while self-heal is on, retrying stuck
// pauses and resumes only.
func idleSweepModes(cfg config.AppConfig, flags features.Flags) (run, judgesIdle bool) {
	return cfg.AutoPauseEnabled || selfHealOn(cfg, flags), cfg.AutoPauseEnabled
}
