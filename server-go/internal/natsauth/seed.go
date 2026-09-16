package natsauth

import (
	"context"
	"fmt"
)

// SeedStore is the write half of the credential store used at boot.
type SeedStore interface {
	UpsertNatsCredential(ctx context.Context, principal, projectID, passwordHash string) error
}

// SeedServicePrincipals makes the platform's own bus credentials usable.
//
// The chart mints one password per service into a Secret; every service —
// including this one — reads its password from there. The callout, though,
// authenticates against the database, so each boot re-derives the hashes
// from the Secret. That makes the Secret the single source of truth and
// makes rotation a matter of changing it and restarting.
//
// Principals with a blank password are skipped, so a partially configured
// platform seeds what it has instead of failing outright.
func SeedServicePrincipals(ctx context.Context, store SeedStore, passwords map[string]string) error {
	if store == nil {
		return fmt.Errorf("nats seed: credential store required")
	}
	for _, principal := range []string{PrincipalProvisioning, PrincipalGraphQL, PrincipalPgDog} {
		password := passwords[principal]
		if password == "" {
			continue
		}
		hash, err := HashPassword(password)
		if err != nil {
			return fmt.Errorf("nats seed: hash %s: %w", principal, err)
		}
		// Service principals are not scoped to a project.
		if err := store.UpsertNatsCredential(ctx, principal, "", hash); err != nil {
			return fmt.Errorf("nats seed: store %s: %w", principal, err)
		}
	}
	return nil
}
