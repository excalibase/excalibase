package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fakeTierStore is an in-memory storage.TierConfigStore for resolver tests.
type fakeTierStore struct {
	m map[domain.TierType]config.TierConfig
}

func (f fakeTierStore) ListTierConfigs(_ context.Context) (map[domain.TierType]config.TierConfig, error) {
	return f.m, nil
}

func (f fakeTierStore) GetTierConfig(_ context.Context, tier domain.TierType) (config.TierConfig, bool, error) {
	tc, ok := f.m[tier]
	return tc, ok, nil
}

func (f fakeTierStore) UpsertTierConfig(_ context.Context, tier domain.TierType, tc config.TierConfig) error {
	f.m[tier] = tc
	return nil
}

func TestTierConfig_PrefersDBOverDefaults(t *testing.T) {
	override := config.TierConfig{MaxProjects: 99, Instances: 2, StorageSize: "200Gi", Memory: "32Gi", CPU: "8", StatementTimeout: "5s"}
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetTierStore(fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Standard: override}})

	got, err := svc.tierConfig(context.Background(), domain.Standard)
	if err != nil {
		t.Fatalf("tierConfig: %v", err)
	}
	if got != override {
		t.Errorf("expected DB override %+v, got %+v", override, got)
	}
}

func TestTierConfig_FallsBackWhenTierAbsentFromStore(t *testing.T) {
	// Store has no row for FREE → resolver must fall back to the hardcoded default.
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetTierStore(fakeTierStore{m: map[domain.TierType]config.TierConfig{}})

	got, err := svc.tierConfig(context.Background(), domain.Free)
	if err != nil {
		t.Fatalf("tierConfig: %v", err)
	}
	want, _ := config.GetTierConfig(domain.Free)
	if got != want {
		t.Errorf("expected hardcoded default %+v, got %+v", want, got)
	}
}

func TestTierConfig_FallsBackWhenNoStore(t *testing.T) {
	svc := NewProvisioningService(nil, nil, nil) // no tierStore wired
	got, err := svc.tierConfig(context.Background(), domain.Enterprise)
	if err != nil {
		t.Fatalf("tierConfig: %v", err)
	}
	want, _ := config.GetTierConfig(domain.Enterprise)
	if got != want {
		t.Errorf("expected hardcoded default %+v, got %+v", want, got)
	}
}
