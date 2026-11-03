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
		Step: domain.MajorUpgradeStepChecking, RequestedBy: "u-1", Owner: "driver-1",
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
	if err := store.UpdateMajorUpgrade(ctx, run, nil); err != nil {
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
	if err := store.MarkMajorUpgradeConverted(ctx, run.ID, "mu-conv", "17", "driver-1", at); err != nil {
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
	if err := store.MarkMajorUpgradeConverted(ctx, run.ID, "mu-conv", "17", "driver-1", at); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
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
	if err := store.UpdateMajorUpgrade(ctx, done, nil); err != nil {
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
	ageHeartbeat(t, store, "mu-hb1", time.Hour)
	if err := store.TouchMajorUpgrade(ctx, "mu-hb1", "driver-1"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	running, err := store.RunningMajorUpgrades(ctx)
	if err != nil || len(running) != 1 || dbNow(t, store).Sub(running[0].HeartbeatAt) > time.Minute {
		t.Fatalf("running = %+v, %v", running, err)
	}
	finished := time.Now().UTC()
	run.Status, run.FinishedAt = domain.MajorUpgradeCompleted, &finished
	if err := store.UpdateMajorUpgrade(ctx, run, nil); err != nil {
		t.Fatalf("finish: %v", err)
	}
	run.Status = domain.MajorUpgradeFailed
	if err := store.UpdateMajorUpgrade(ctx, run, nil); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("a finished run was rewritten: %v", err)
	}
	if err := store.TouchMajorUpgrade(ctx, "mu-hb1", "driver-1"); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("a finished run took a heartbeat: %v", err)
	}
}

// ageHeartbeat moves the run's heartbeat back by age on the database's clock.
func ageHeartbeat(t *testing.T, store *Store, id string, age time.Duration) {
	t.Helper()
	if _, err := store.db.Exec(`UPDATE database_major_upgrades SET heartbeat_at = now() - make_interval(secs => $2) WHERE id = $1`,
		id, age.Seconds()); err != nil {
		t.Fatal(err)
	}
}

func dbNow(t *testing.T, store *Store) time.Time {
	t.Helper()
	var now time.Time
	if err := store.db.QueryRow(`SELECT now()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}

func startClaimable(t *testing.T, store *Store, projectID, id string) *domain.MajorUpgrade {
	t.Helper()
	seedUpgradeProject(t, store, projectID, "16")
	run := runningUpgrade(id, projectID, "16", "17")
	if err := store.StartMajorUpgrade(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return run
}

// A stale run is claimed once, and only by one actor; the claimer then
// owns every write to it.
func TestMajorUpgrades_ClaimFencesTheFormerOwner(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	run := startClaimable(t, store, "mu-claim", "mu-cl1")
	now := time.Now().UTC()
	if claimed, err := store.ClaimMajorUpgrade(ctx, run.ID, "fresh-check", time.Minute); err != nil || claimed != nil {
		t.Fatalf("claim of a live run = %+v, %v", claimed, err)
	}
	ageHeartbeat(t, store, run.ID, 10*time.Minute)
	claimed, err := store.ClaimMajorUpgrade(ctx, run.ID, "recovery-1", time.Minute)
	if err != nil || claimed == nil || claimed.Owner != "recovery-1" || claimed.ID != run.ID {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if again, err := store.ClaimMajorUpgrade(ctx, run.ID, "recovery-2", time.Minute); err != nil || again != nil {
		t.Fatalf("second claim = %+v, %v", again, err)
	}
	if err := store.TouchMajorUpgrade(ctx, run.ID, "driver-1"); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("former owner touch = %v", err)
	}
	if err := store.UpdateMajorUpgrade(ctx, run, nil); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("former owner update = %v", err)
	}
	if err := store.MarkMajorUpgradeConverted(ctx, run.ID, "mu-claim", "17", "driver-1", now); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
		t.Fatalf("former owner mark = %v", err)
	}
}

// After a claim the new owner writes the run like its driver did.
func TestMajorUpgrades_TheNewOwnerWritesAfterAClaim(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	run := startClaimable(t, store, "mu-own", "mu-own1")
	ageHeartbeat(t, store, run.ID, 10*time.Minute)
	claimed, err := store.ClaimMajorUpgrade(ctx, run.ID, "recovery-1", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err := store.TouchMajorUpgrade(ctx, run.ID, "recovery-1"); err != nil {
		t.Fatalf("touch = %v", err)
	}
	claimed.Step = domain.MajorUpgradeStepConverting
	if err := store.UpdateMajorUpgrade(ctx, claimed, &storage.MajorUpgradeProject{Status: "UPGRADING", Step: claimed.Step}); err != nil {
		t.Fatalf("update = %v", err)
	}
	if err := store.MarkMajorUpgradeConverted(ctx, run.ID, "mu-own", "17", "recovery-1", time.Now()); err != nil {
		t.Fatalf("mark = %v", err)
	}
	latest, _ := store.LatestMajorUpgrade(ctx, "mu-own")
	if latest.Owner != "recovery-1" || !latest.Converted || latest.Step != domain.MajorUpgradeStepConverting {
		t.Fatalf("run = %+v", latest)
	}
}

// Staleness is judged and heartbeats are stamped on the database's clock,
// so an app clock that runs fast or slow cannot claim a live run or keep a
// dead one.
func TestMajorUpgrades_HeartbeatsUseTheDatabaseClock(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	run := startClaimable(t, store, "mu-clock", "mu-clk1")
	latest, _ := store.LatestMajorUpgrade(ctx, "mu-clock")
	if dbNow(t, store).Sub(latest.HeartbeatAt) > time.Minute {
		t.Fatalf("start heartbeat %s is not the database's now", latest.HeartbeatAt)
	}
	if claimed, _ := store.ClaimMajorUpgrade(ctx, run.ID, "recovery-1", 3*time.Minute); claimed != nil {
		t.Fatal("a run marked alive just now was claimed")
	}
	ageHeartbeat(t, store, run.ID, 4*time.Minute)
	claimed, err := store.ClaimMajorUpgrade(ctx, run.ID, "recovery-1", 3*time.Minute)
	if err != nil || claimed == nil || dbNow(t, store).Sub(claimed.HeartbeatAt) > time.Minute {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	ageHeartbeat(t, store, run.ID, 4*time.Minute)
	if err := store.MarkMajorUpgradeConverted(ctx, run.ID, "mu-clock", "17", "recovery-1", time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	latest, _ = store.LatestMajorUpgrade(ctx, "mu-clock")
	if dbNow(t, store).Sub(latest.HeartbeatAt) > time.Minute {
		t.Fatalf("mark heartbeat %s is not the database's now", latest.HeartbeatAt)
	}
}

// A run's write and its project's status land together, and a converted
// run ends only as COMPLETED.
func TestMajorUpgrades_UpdateWritesTheProjectAndKeepsAConvertedRunForward(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	seedUpgradeProject(t, store, "mu-tx", "16")
	run := runningUpgrade("mu-tx1", "mu-tx", "16", "17")
	if err := store.StartMajorUpgrade(ctx, run); err != nil {
		t.Fatal(err)
	}
	run.Step = domain.MajorUpgradeStepConverting
	if err := store.UpdateMajorUpgrade(ctx, run, &storage.MajorUpgradeProject{Status: "UPGRADING", Step: run.Step}); err != nil {
		t.Fatal(err)
	}
	if inst, _ := store.FindByProjectID("mu-tx"); inst.Status != "UPGRADING" || inst.CurrentStep != run.Step {
		t.Fatalf("project = %s at %s", inst.Status, inst.CurrentStep)
	}
	if err := store.MarkMajorUpgradeConverted(ctx, run.ID, "mu-tx", "17", "driver-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{domain.MajorUpgradeFailed, domain.MajorUpgradeRolledBack} {
		ended := *run
		ended.Status = outcome
		if err := store.UpdateMajorUpgrade(ctx, &ended, &storage.MajorUpgradeProject{Status: "ACTIVE"}); !errors.Is(err, storage.ErrMajorUpgradeNotRunning) {
			t.Fatalf("ending a converted run %s = %v", outcome, err)
		}
	}
	if inst, _ := store.FindByProjectID("mu-tx"); inst.Status != "UPGRADING" {
		t.Fatalf("project = %s after a refused write", inst.Status)
	}
	run.Status = domain.MajorUpgradeCompleted
	if err := store.UpdateMajorUpgrade(ctx, run, &storage.MajorUpgradeProject{Status: "ACTIVE"}); err != nil {
		t.Fatalf("complete = %v", err)
	}
}
