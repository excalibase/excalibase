package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

var errSetupTokenRequired = errors.New("SETUP_TOKEN is required on Kubernetes until the first platform admin exists " +
	"(the platform-aio chart sets it from the platform-setup-token Secret); refusing to generate one and print it to the log")

// establishFirstAdminSetupToken sets up the one-time first-admin token (EXC-451)
// while the platform has no admin. The operator's SETUP_TOKEN is adopted and
// never logged. Without one, Kubernetes refuses to start (GA risk 44: a token
// in the log is readable by anyone with log access); only the docker
// provisioner, where docker-compose.aio.yml already requires SETUP_TOKEN, falls
// back to generating one and logging it once for a bare dev run.
func establishFirstAdminSetupToken(ctx context.Context, cfg config.AppConfig, store storage.SetupTokenStore,
	presetToken string, logf func(format string, args ...any)) error {
	if presetToken == "" && cfg.ProvisionerMode != "docker" {
		hasAdmin, err := store.HasPlatformAdmin(ctx)
		if err != nil {
			return fmt.Errorf("check platform admin: %w", err)
		}
		if hasAdmin {
			return nil
		}
		return errSetupTokenRequired
	}
	raw, err := auth.BootstrapSetupToken(ctx, store, presetToken)
	if err != nil || raw == "" {
		return err
	}
	logf("=== FIRST-ADMIN SETUP TOKEN ===")
	logf("First-admin setup token: %s", raw)
	logf("Use it once in POST /api/auth/register as \"setupToken\" to create the platform admin.")
	logf("================================")
	return nil
}
