package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// erroringTierStore fails every read, to prove a broken tier table fails the
// operation rather than sizing a project from values nobody chose.
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

func TestTierConfig_StoreErrorFails(t *testing.T) {
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetTierStore(erroringTierStore{})

	if _, err := svc.TierConfig(context.Background(), domain.Free); !errors.Is(err, ErrTierConfigUnavailable) {
		t.Fatalf("a tier store that cannot be read must fail resolution, got %v", err)
	}
}

func TestProvision_RefusesWhenTheTierStoreFails(t *testing.T) {
	svc, store, mock := setupProvisioningTest(t)
	svc.SetTierStore(erroringTierStore{})

	_, err := provisionInto(t, svc, "org", "p")
	if !errors.Is(err, ErrTierConfigUnavailable) {
		t.Fatalf("Provision = %v, want ErrTierConfigUnavailable", err)
	}
	if all, _ := store.FindAll(); len(all) != 0 {
		t.Errorf("no project may be recorded: %v", all)
	}
	if len(mock.CRDs) != 0 || len(mock.Namespaces) != 0 {
		t.Errorf("nothing may be created: crds=%v ns=%v", mock.CRDs, mock.Namespaces)
	}
}

func TestTierConfig_UnknownTierErrors(t *testing.T) {
	svc := NewProvisioningService(nil, nil, nil)
	if _, err := svc.TierConfig(context.Background(), domain.TierType("PLATINUM")); err == nil {
		t.Fatal("expected an error for an unknown tier")
	}
}
