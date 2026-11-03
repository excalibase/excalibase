package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func archivesWorld(t *testing.T, version string, runs ...domain.MajorUpgrade) (*MajorUpgradeArchives, *domain.DatabaseInstance) {
	t.Helper()
	instances, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inst := &domain.DatabaseInstance{ProjectID: "p", OrgID: "o", PostgresVersion: version, Status: string(domain.StatusActive)}
	if err := instances.Create(inst); err != nil {
		t.Fatal(err)
	}
	store := newMemMajorUpgrades(instances)
	for i := range runs {
		run := runs[i]
		run.ProjectID = "p"
		run.Status = domain.MajorUpgradeCompleted
		store.runs[run.ID] = &run
	}
	return NewMajorUpgradeArchives(store), inst
}

func hourOf(hour int) *time.Time {
	t := time.Date(2026, 10, 29, hour, 0, 0, 0, time.UTC)
	return &t
}

func convertedRun(id, from, to string, stopped, converted int) domain.MajorUpgrade {
	return domain.MajorUpgrade{ID: id, FromVersion: from, ToVersion: to, Converted: true,
		WritesStoppedAt: hourOf(stopped), ConvertedAt: hourOf(converted)}
}

func TestArchives_ANeverUpgradedProjectUsesTheOriginalArchive(t *testing.T) {
	archives, inst := archivesWorld(t, "16")
	original, err := archives.OriginalMajor(context.Background(), inst)
	if err != nil || original != "16" {
		t.Fatalf("original = %q, %v", original, err)
	}
	if archive, _ := archives.Archive(context.Background(), inst, "16"); archive != "" {
		t.Fatalf("archive = %q", archive)
	}
}

func TestArchives_EachUpgradedMajorHasItsOwnArchive(t *testing.T) {
	archives, inst := archivesWorld(t, "17", convertedRun("a", "14", "16", 1, 2), convertedRun("b", "16", "17", 5, 6))
	for major, want := range map[string]string{"14": "", "16": "pg16", "17": "pg17"} {
		if got, err := archives.Archive(context.Background(), inst, major); err != nil || got != want {
			t.Errorf("Archive(%s) = %q, %v; want %q", major, got, err, want)
		}
	}
}

func TestArchives_ANilArchivesIsTheOriginalLayout(t *testing.T) {
	var archives *MajorUpgradeArchives
	inst := &domain.DatabaseInstance{ProjectID: "p", PostgresVersion: "16"}
	if got, err := archives.Archive(context.Background(), inst, "16"); err != nil || got != "" {
		t.Fatalf("Archive = %q, %v", got, err)
	}
	if got, _ := archives.OriginalMajor(context.Background(), inst); got != "16" {
		t.Fatalf("OriginalMajor = %q", got)
	}
}

func TestRestorePoint(t *testing.T) {
	runs := []domain.MajorUpgrade{convertedRun("a", "14", "16", 2, 3), convertedRun("b", "16", "17", 8, 9)}
	backupMajors := map[string]string{"b-14": "14", "b-16": "16", "b-17": "17", "b-unknown": ""}
	majorOf := func(_ context.Context, _ *domain.DatabaseInstance, id string) (string, error) {
		major, ok := backupMajors[id]
		if !ok {
			return "", errors.New("no such backup")
		}
		return major, nil
	}
	cases := []struct {
		name      string
		req       domain.RestoreRequest
		wantMajor string
		wantTime  bool
	}{
		{"latest is the current major", domain.RestoreRequest{}, "17", false},
		{"a backup from before the first upgrade", domain.RestoreRequest{BackupID: "b-14"}, "14", false},
		{"a backup between the upgrades", domain.RestoreRequest{BackupID: "b-16"}, "16", false},
		{"a backup on the current major", domain.RestoreRequest{BackupID: "b-17"}, "17", false},
		{"a backup that does not say is the original major", domain.RestoreRequest{BackupID: "b-unknown"}, "14", false},
		{"a time before the first upgrade", domain.RestoreRequest{TargetTime: zonedHour(1)}, "14", true},
		{"a time inside the first upgrade is the end of 14", domain.RestoreRequest{TargetTime: zonedHour(2)}, "14", false},
		{"a time between the upgrades", domain.RestoreRequest{TargetTime: zonedHour(5)}, "16", true},
		{"a time inside the second upgrade is the end of 16", domain.RestoreRequest{TargetTime: zonedHour(8)}, "16", false},
		{"a time after the last upgrade", domain.RestoreRequest{TargetTime: zonedHour(10)}, "17", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archives, inst := archivesWorld(t, "17", runs...)
			view, req, err := archives.RestorePoint(context.Background(), inst, tc.req, majorOf)
			if err != nil {
				t.Fatal(err)
			}
			if view.PostgresVersion != tc.wantMajor {
				t.Fatalf("major = %s, want %s", view.PostgresVersion, tc.wantMajor)
			}
			if (req.TargetTime != nil) != tc.wantTime {
				t.Fatalf("target time kept = %v, want %v", req.TargetTime != nil, tc.wantTime)
			}
			if inst.PostgresVersion != "17" {
				t.Fatal("the project row was modified")
			}
		})
	}
}

func TestRestorePoint_AnUnknownBackupIsAnError(t *testing.T) {
	archives, inst := archivesWorld(t, "17", convertedRun("a", "16", "17", 2, 3))
	_, _, err := archives.RestorePoint(context.Background(), inst, domain.RestoreRequest{BackupID: "nope"},
		func(context.Context, *domain.DatabaseInstance, string) (string, error) {
			return "", errors.New("missing")
		})
	if err == nil {
		t.Fatal("an unknown backup resolved")
	}
}

func TestRestorePoint_NothingChangesForANeverUpgradedProject(t *testing.T) {
	archives, inst := archivesWorld(t, "16")
	req := domain.RestoreRequest{TargetTime: zonedHour(5)}
	view, got, err := archives.RestorePoint(context.Background(), inst, req, nil)
	if err != nil || view != inst || got.TargetTime == nil {
		t.Fatalf("view %p inst %p req %+v err %v", view, inst, got, err)
	}
}

func zonedHour(hour int) *domain.ZonedTime {
	return &domain.ZonedTime{Time: *hourOf(hour)}
}
