package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// TierConfigStore persists per-tier resource specs (CPU / RAM / storage /
// replicas) so platform admins can tune them from the admin UI without a
// redeploy. The hardcoded config.GetTierConfig map remains the fallback when a
// tier row is absent, so a missing/empty table never breaks provisioning.
type TierConfigStore interface {
	// ListTierConfigs returns every stored tier keyed by its TierType.
	ListTierConfigs(ctx context.Context) (map[domain.TierType]config.TierConfig, error)
	// GetTierConfig returns the stored spec for one tier. The bool is false
	// (with nil error) when no row exists — callers fall back to defaults.
	GetTierConfig(ctx context.Context, tier domain.TierType) (config.TierConfig, bool, error)
	// UpsertTierConfig inserts or replaces the spec for one tier.
	UpsertTierConfig(ctx context.Context, tier domain.TierType, tc config.TierConfig) error
}
