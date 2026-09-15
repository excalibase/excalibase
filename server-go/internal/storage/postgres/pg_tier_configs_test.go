//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestTierConfigs_SeededDefaults(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	tc, ok, err := store.GetTierConfig(ctx, domain.Free)
	if err != nil {
		t.Fatalf("GetTierConfig: %v", err)
	}
	if !ok {
		t.Fatal("FREE tier not seeded by migration")
	}
	if tc.Instances != 1 || tc.CPU != "0.5" || tc.Memory != "512Mi" || tc.StorageSize != "5Gi" {
		t.Errorf("FREE defaults wrong: %+v", tc)
	}

	all, err := store.ListTierConfigs(ctx)
	if err != nil {
		t.Fatalf("ListTierConfigs: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 seeded tiers, got %d", len(all))
	}
}

func TestTierConfigs_UpsertRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	want := config.TierConfig{MaxProjects: 10, Instances: 3, StorageSize: "100Gi", Memory: "8Gi", CPU: "4", BackupEnabled: true}
	if err := store.UpsertTierConfig(ctx, domain.Standard, want); err != nil {
		t.Fatalf("UpsertTierConfig: %v", err)
	}

	got, ok, err := store.GetTierConfig(ctx, domain.Standard)
	if err != nil || !ok {
		t.Fatalf("GetTierConfig after upsert: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestTierConfigs_GetMissingReturnsNotFound(t *testing.T) {
	store := testStore(t)
	_, ok, err := store.GetTierConfig(context.Background(), domain.TierType("NOPE"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false for unknown tier")
	}
}

func TestTierConfigs_AutoPauseColumn(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	free, ok, err := store.GetTierConfig(ctx, domain.Free)
	if err != nil || !ok {
		t.Fatalf("GetTierConfig FREE: ok=%v err=%v", ok, err)
	}
	if free.AutoPauseAfterDays != 7 {
		t.Errorf("FREE must be seeded with autoPauseAfterDays=7, got %d", free.AutoPauseAfterDays)
	}
	standard, _, _ := store.GetTierConfig(ctx, domain.Standard)
	if standard.AutoPauseAfterDays != 0 {
		t.Errorf("STANDARD must never auto-pause by default, got %d", standard.AutoPauseAfterDays)
	}

	want := config.TierConfig{MaxProjects: 1, Instances: 1, StorageSize: "5Gi", Memory: "512Mi", CPU: "0.5", AutoPauseAfterDays: 3}
	if err := store.UpsertTierConfig(ctx, domain.Free, want); err != nil {
		t.Fatalf("UpsertTierConfig: %v", err)
	}
	got, _, _ := store.GetTierConfig(ctx, domain.Free)
	if got != want {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}
