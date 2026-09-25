package auth

import (
	"context"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/storage"
)

// MinSetupTokenLength is the shortest SETUP_TOKEN BootstrapSetupToken will
// accept from an operator. Shorter is refused rather than silently used, the
// same posture as a generated token (32 random bytes).
const MinSetupTokenLength = 32

// BootstrapSetupToken establishes the one-time first-admin setup token
// (EXC-451), unless a platform admin already exists — in which case it does
// nothing and returns "".
//
// presetToken, when non-empty, is an operator-supplied token (the chart
// bootstrap Job's SETUP_TOKEN env) adopted as the one-time token instead of
// generating one; only its hash is stored, and it is never returned, so the
// caller can never log a token the operator already knows. presetToken
// shorter than MinSetupTokenLength is refused with an error rather than
// silently used.
//
// presetToken == "" keeps today's behaviour: a fresh token is generated,
// its hash stored, and the raw value returned once for the caller to log.
func BootstrapSetupToken(ctx context.Context, store storage.SetupTokenStore, presetToken string) (string, error) {
	hasAdmin, err := store.HasPlatformAdmin(ctx)
	if err != nil {
		return "", fmt.Errorf("check platform admin: %w", err)
	}
	if hasAdmin {
		return "", nil
	}

	if presetToken != "" {
		if len(presetToken) < MinSetupTokenLength {
			return "", fmt.Errorf("SETUP_TOKEN must be at least %d characters, got %d", MinSetupTokenLength, len(presetToken))
		}
		if err := store.StoreSetupTokenHash(ctx, HashToken(presetToken)); err != nil {
			return "", fmt.Errorf("store setup token: %w", err)
		}
		return "", nil
	}

	raw := GenerateSetupToken()
	if err := store.StoreSetupTokenHash(ctx, HashToken(raw)); err != nil {
		return "", fmt.Errorf("store setup token: %w", err)
	}
	return raw, nil
}
