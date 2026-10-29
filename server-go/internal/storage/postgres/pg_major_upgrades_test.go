//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func seedUpgradeProject(t *testing.T, store *Store, projectID, version string) {
	t.Helper()
	inst := &domain.DatabaseInstance{
		ProjectID: projectID, ProjectName: projectID, OrgID: "org-1", OwnerID: "u-1",
		DBType: domain.PostgreSQL, Tier: domain.Free, Namespace: "org-1-" + projectID,
		PostgresVersion: version, Status: string(domain.StatusActive),
	}
	if err := store.Create(inst); err != nil {
		t.Fatalf("create %s: %v", projectID, err)
	}
}

func runningUpgrade(id, projectID, from, to string) *domain.MajorUpgrade {
	return &domain.MajorUpgrade{
		ID: id, ProjectID: projectID, Engine: string(domain.PostgreSQL),
		FromVersion: from, ToVersion: to, Status: domain.MajorUpgradeRunning,
		Step: domain.MajorUpgradeStepChecking, RequestedBy: "u-1",
	}
}

func TestMajorUpgrades_OneRunningPerProject(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	seedUpgradeProject(t, store, "mu-one", "16")

	if err := store.StartMajorUpgrade(ctx, runningUpgrade("mu-1", "mu-one", "16", "17")); err != nil {
		t.Fatalf("start: %v", err)
	}
	err := store.StartMajorUpgrade(ctx, runningUpgrade("mu-2", "mu-one", "16", "17"))
	if !errors.Is(err, storage.ErrMajorUpgradeRunning) {
		t.Fatalf("second start = %v, want ErrMajorUpgradeRunning", err)
	}
}

func TestMajorUpgrades_UpdateAndLatest(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	seedUpgradeProject(t, store, "mu-up", "16")

	none, err := store.LatestMajorUpgrade(ctx, "mu-up")
	if err != nil || none != nil {
		t.Fatalf("latest before any = %v, %v; want nil, nil", none, err)
	}
	run := runningUpgrade("mu-u1", "mu-up", "16", "17")
	if err := store.StartMajorUpgrade(ctx, run); err != nil {
		t.Fatalf("start: %v", err)
	}
	finished := time.Now().UTC().Truncate(time.Millisecond)
	run.Status = domain.MajorUpgradeRolledBack
	run.Step = domain.MajorUpgradeStepRollingBack
	run.SafetyBackupID = "backup-1"
	run.Error = "the upgrade did not complete, so the database was put back on 16"
	run.Warnings = []string{"statistics were not refreshed"}
	run.FinishedAt = &finished
	run.WritesStoppedAt = &finished
	if err := store.UpdateMajorUpgrade(ctx, run); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.LatestMajorUpgrade(ctx, "mu-up")
	if err != nil || got == nil {
		t.Fatalf("latest = %v, %v", got, err)
	}
	if got.Status != domain.MajorUpgradeRolledBack || got.SafetyBackupID != "backup-1" || got.Error != run.Error ||
		len(got.Warnings) != 1 || got.FinishedAt == nil || got.WritesStoppedAt == nil || got.Converted || got.RequestedBy != "u-1" {
		t.Fatalf("latest = %+v", got)
	}
	// A finished run frees the project for the next one.
	if err := store.StartMajorUpgrade(ctx, runningUpgrade("mu-u2", "mu-up", "16", "17")); err != nil {
		t.Fatalf("start after a finished run: %v", err)
	}
	latest, _ := store.LatestMajorUpgrade(ctx, "mu-up")
	if latest.ID != "mu-u2" {
		t.Fatalf("latest = %s, want mu-u2", latest.ID)
	}
}

func TestMajorUpgrades_MarkConvertedMovesTheProjectVersionInTheSameWrite(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	seedUpgradeProject(t, store, "mu-conv", "15")
	run := runningUpgrade("mu-c1", "mu-conv", "15", "17")
	if err := store.StartMajorUpgrade(ctx, run); err != nil {
		t.Fatalf("start: %v", err)
	}
	at := time.Now().UTC()
	if err := store.MarkMajorUpgradeConverted(ctx, run.ID, "mu-conv", "17", at); err != nil {
		t.Fatalf("mark converted: %v", err)
	}
	inst, err := store.FindByProjectID("mu-conv")
	if err != nil || inst.PostgresVersion != "17" {
		t.Fatalf("project version = %v (%v), want 17", inst, err)
	}
	converted, err := store.ConvertedMajorUpgrades(ctx, "mu-conv")
	if err != nil || len(converted) != 1 || !converted[0].Converted || converted[0].ConvertedAt == nil {
		t.Fatalf("converted = %+v, %v", converted, err)
	}
	// A second mark is refused: the version moved once.
	if err := store.MarkMajorUpgradeConverted(ctx, run.ID, "mu-conv", "17", at); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("second mark = %v, want ErrMajorUpgradeNotRunning", err)
	}
}

func TestMajorUpgrades_RunningAcrossProjects(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	seedUpgradeProject(t, store, "mu-r1", "16")
	seedUpgradeProject(t, store, "mu-r2", "16")
	if err := store.StartMajorUpgrade(ctx, runningUpgrade("mu-ra", "mu-r1", "16", "17")); err != nil {
		t.Fatal(err)
	}
	done := runningUpgrade("mu-rb", "mu-r2", "16", "17")
	if err := store.StartMajorUpgrade(ctx, done); err != nil {
		t.Fatal(err)
	}
	done.Status = domain.MajorUpgradeFailed
	if err := store.UpdateMajorUpgrade(ctx, done); err != nil {
		t.Fatal(err)
	}
	running, err := store.RunningMajorUpgrades(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, run := range running {
		ids[run.ID] = true
	}
	if !ids["mu-ra"] || ids["mu-rb"] {
		t.Fatalf("running = %v, want mu-ra only", ids)
	}
}

func TestMajorUpgrades_RowsGoWithTheProject(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	seedUpgradeProject(t, store, "mu-del", "16")
	if err := store.StartMajorUpgrade(ctx, runningUpgrade("mu-d1", "mu-del", "16", "17")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `DELETE FROM database_instances WHERE project_id = 'mu-del'`); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.LatestMajorUpgrade(ctx, "mu-del"); got != nil {
		t.Fatalf("upgrade row outlived its project: %+v", got)
	}
}

func TestBackupRecords_KeepTheMajorTheyWereTakenOn(t *testing.T) {
	store := testStore(t)
	br := NewBackupRecords(store)
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := br.Save(ctx, &domain.BackupRecord{ID: "pv1", ProjectID: "pv", Timestamp: now, Type: "MANUAL",
		Status: "COMPLETED", PostgresVersion: "16"}); err != nil {
		t.Fatal(err)
	}
	if err := br.Save(ctx, &domain.BackupRecord{ID: "pv0", ProjectID: "pv", Timestamp: now, Type: "MANUAL", Status: "COMPLETED"}); err != nil {
		t.Fatal(err)
	}
	got, err := br.ListByProject(ctx, "pv")
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]string{}
	for _, r := range got {
		versions[r.ID] = r.PostgresVersion
	}
	if versions["pv1"] != "16" || versions["pv0"] != "" {
		t.Fatalf("versions = %v", versions)
	}
}

func TestMajorUpgrades_HeartbeatAndNoWriteAfterTheEnd(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	seedUpgradeProject(t, store, "mu-hb", "16")
	run := runningUpgrade("mu-hb1", "mu-hb", "16", "17")
	run.StartedAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if err := store.StartMajorUpgrade(ctx, run); err != nil {
		t.Fatalf("start: %v", err)
	}
	beat := time.Now().UTC().Truncate(time.Millisecond)
	if err := store.TouchMajorUpgrade(ctx, "mu-hb1", beat); err != nil {
		t.Fatalf("touch: %v", err)
	}
	running, err := store.RunningMajorUpgrades(ctx)
	if err != nil || len(running) != 1 || !running[0].HeartbeatAt.Equal(beat) {
		t.Fatalf("running = %+v, %v", running, err)
	}
	finished := time.Now().UTC()
	run.Status, run.FinishedAt = domain.MajorUpgradeCompleted, &finished
	if err := store.UpdateMajorUpgrade(ctx, run); err != nil {
		t.Fatalf("finish: %v", err)
	}
	run.Status = domain.MajorUpgradeFailed
	if err := store.UpdateMajorUpgrade(ctx, run); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("a finished run was rewritten: %v", err)
	}
	if err := store.TouchMajorUpgrade(ctx, "mu-hb1", time.Now()); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("a finished run took a heartbeat: %v", err)
	}
}
