package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// erroringTierStore fails every read, to prove a broken tier table degrades to
// the config defaults instead of blocking provisioning. (The happy paths of the
// resolver are covered in tier_resolver_test.go via fakeTierStore.)
type erroringTierStore struct{}

func (erroringTierStore) ListTierConfigs(context.Context) (map[domain.TierType]config.TierConfig, error) {
	return nil, errors.New("db down")
}

func (erroringTierStore) GetTierConfig(context.Context, domain.TierType) (config.TierConfig, bool, error) {
	return config.TierConfig{}, false, errors.New("db down")
}

func (erroringTierStore) UpsertTierConfig(context.Context, domain.TierType, config.TierConfig) error {
	return errors.New("db down")
}

// The exported wrapper is what the capacity report calls; it must return the
// same store-first answer the unexported resolver gives admission.
func TestTierConfig_ExportedWrapperPrefersStoreRow(t *testing.T) {
	edited := config.TierConfig{MaxProjects: 1, Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.25", StatementTimeout: "10s"}
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetTierStore(fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Free: edited}})

	got, err := svc.TierConfig(context.Background(), domain.Free)
	if err != nil {
		t.Fatalf("TierConfig: %v", err)
	}
	if got != edited {
		t.Fatalf("expected the admin-edited row %+v, got %+v", edited, got)
	}
}

func TestTierConfig_FallsBackWhenStoreErrors(t *testing.T) {
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetTierStore(erroringTierStore{})

	got, err := svc.TierConfig(context.Background(), domain.Free)
	if err != nil {
		t.Fatalf("a store error must not fail resolution: %v", err)
	}
	want, _ := config.GetTierConfig(domain.Free)
	if got != want {
		t.Fatalf("expected config default when the store errors: got %+v want %+v", got, want)
	}
}

func TestTierConfig_UnknownTierErrors(t *testing.T) {
	svc := NewProvisioningService(nil, nil, nil)
	if _, err := svc.TierConfig(context.Background(), domain.TierType("PLATINUM")); err == nil {
		t.Fatal("expected an error for an unknown tier")
	}
}
