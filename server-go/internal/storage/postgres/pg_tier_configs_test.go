//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
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
	for tier, stored := range all {
		builtin, _ := config.GetTierConfig(tier)
		assertSeededLikeBuiltin(t, tier, stored, builtin)
	}
}

// assertSeededLikeBuiltin checks a seeded plan against the built-in catalogue:
// instances (one per node), growth ceiling (EXC-492), app disk cap (EXC-523),
// app count (EXC-524) and Sites limits (EXC-620).
func assertSeededLikeBuiltin(t *testing.T, tier domain.TierType, stored, builtin config.TierConfig) {
	t.Helper()
	if stored.Instances != builtin.Instances {
		t.Errorf("%s: stored instances %d, built-in %d", tier, stored.Instances, builtin.Instances)
	}
	if stored.MaxStorageSize != builtin.MaxStorageSize {
		t.Errorf("%s: stored max disk %q, built-in %q", tier, stored.MaxStorageSize, builtin.MaxStorageSize)
	}
	if stored.MaxApps != builtin.MaxApps {
		t.Errorf("%s: stored max apps %d, built-in %d", tier, stored.MaxApps, builtin.MaxApps)
	}
	if stored.MaxAppDiskSize != builtin.MaxAppDiskSize {
		t.Errorf("%s: stored app disk cap %q, built-in %q", tier, stored.MaxAppDiskSize, builtin.MaxAppDiskSize)
	}
	if stored.Sites != builtin.Sites {
		t.Errorf("%s: stored site limits %+v, built-in %+v", tier, stored.Sites, builtin.Sites)
	}
}

func TestTierConfigs_UpsertRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	want := config.TierConfig{MaxProjects: 10, Instances: 3, StorageSize: "100Gi", MaxStorageSize: "300Gi", MaxAppDiskSize: "40Gi", MaxApps: 7, Memory: "8Gi", CPU: "4", BackupEnabled: true,
		Sites: config.SiteTierLimits{MaxSites: 9, MaxDeployBytes: 200 << 20, MaxFiles: 30_000, MaxFileBytes: 10 << 20, MaxDeploysPerHour: 45}}
	version, err := store.UpsertTierConfig(ctx, domain.Standard, want, nil)
	if err != nil {
		t.Fatalf("UpsertTierConfig: %v", err)
	}
	want.Version = version

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
	version, err := store.UpsertTierConfig(ctx, domain.Free, want, nil)
	if err != nil {
		t.Fatalf("UpsertTierConfig: %v", err)
	}
	want.Version = version
	got, _, _ := store.GetTierConfig(ctx, domain.Free)
	if got != want {
		t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestTierConfigs_EveryStoredTierIsBackedUp(t *testing.T) {
	store := testStore(t)
	all, err := store.ListTierConfigs(context.Background())
	if err != nil {
		t.Fatalf("ListTierConfigs: %v", err)
	}
	for tier, tc := range all {
		if !tc.BackupEnabled {
			t.Errorf("%s is stored without backups", tier)
		}
	}
}

// Two admins save the same plan from one read: the second is refused and
// leaves the first in place; a save from the current version goes through.
func TestTierConfigs_UpsertRefusesAStaleVersion(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	read, _, err := store.GetTierConfig(ctx, domain.Enterprise)
	if err != nil || read.Version < 1 {
		t.Fatalf("seeded version = %d, %v", read.Version, err)
	}
	first := read
	first.MaxProjects = 41
	saved, err := store.UpsertTierConfig(ctx, domain.Enterprise, first, &read.Version)
	if err != nil || saved != read.Version+1 {
		t.Fatalf("first save = %d, %v", saved, err)
	}
	second := read
	second.MaxProjects = 42
	if _, err := store.UpsertTierConfig(ctx, domain.Enterprise, second, &read.Version); !errors.Is(err, storage.ErrTierVersionConflict) ||
		!strings.Contains(err.Error(), fmt.Sprintf("version %d", saved)) {
		t.Fatalf("stale save: err = %v", err)
	}
	if kept, _, _ := store.GetTierConfig(ctx, domain.Enterprise); kept.MaxProjects != 41 || kept.Version != saved {
		t.Fatalf("after the stale save the plan is %+v", kept)
	}
	if again, err := store.UpsertTierConfig(ctx, domain.Enterprise, second, &saved); err != nil || again != saved+1 {
		t.Fatalf("save from the current version = %d, %v", again, err)
	}
}
