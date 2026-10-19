package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-639 follow-up: a restored copy listed no migrations although its schema
// came with the data. The history lives under STORAGE_PATH, not in the backup,
// so the restore has to carry it like the platform-side settings.
func TestMigrationHistoryCopyGivesTheCopyTheSourcesRecordsInOrder(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "projects", "src", "migrations")
	first := &domain.MigrationRecord{ProjectID: "src", Name: "create_links", SQL: "create table links(id int)", Status: "APPLIED"}
	second := &domain.MigrationRecord{ProjectID: "src", Name: "add_slug", SQL: "alter table links add slug text", Status: "APPLIED"}
	now := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	if err := saveMigrationRecord(srcDir, now, first); err != nil {
		t.Fatal(err)
	}
	if err := saveMigrationRecord(srcDir, now.Add(time.Second), second); err != nil {
		t.Fatal(err)
	}

	if err := NewMigrationHistoryFiles(root).CopyMigrationHistory("src", "dst"); err != nil {
		t.Fatalf("CopyMigrationHistory: %v", err)
	}

	svc := NewMigrationService(nil, nil, root)
	copied, err := svc.ListMigrations("dst")
	if err != nil || len(copied) != 2 {
		t.Fatalf("copy lists %+v (%v), want the source's two migrations", copied, err)
	}
	for i, want := range []*domain.MigrationRecord{first, second} {
		if copied[i].ID != want.ID || copied[i].SQL != want.SQL || copied[i].ProjectID != "dst" {
			t.Errorf("copy[%d] = %+v, want %s owned by dst", i, copied[i], want.ID)
		}
	}
	if source, _ := svc.ListMigrations("src"); len(source) != 2 || source[0].ProjectID != "src" {
		t.Errorf("source history changed: %+v", source)
	}
}

func TestMigrationHistoryCopyOfASourceWithNoMigrationsLeavesTheCopyEmpty(t *testing.T) {
	root := t.TempDir()
	if err := NewMigrationHistoryFiles(root).CopyMigrationHistory("src", "dst"); err != nil {
		t.Fatalf("CopyMigrationHistory: %v", err)
	}
	if copied, _ := NewMigrationService(nil, nil, root).ListMigrations("dst"); len(copied) != 0 {
		t.Fatalf("copy lists %+v, want none", copied)
	}
}

// A restore job that is retried copies again onto the same fresh id; the
// second copy must not leave the first one's records twice.
func TestMigrationHistoryCopyReplacesWhatAnEarlierAttemptLeft(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "projects", "src", "migrations")
	if err := saveMigrationRecord(srcDir, time.Now(), &domain.MigrationRecord{ProjectID: "src", SQL: "select 1", Status: "APPLIED"}); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "projects", "dst", "migrations")
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "mig-stale.json"), []byte(`{"id":"mig-stale","projectId":"dst","sql":"x","status":"APPLIED"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	history := NewMigrationHistoryFiles(root)
	for attempt := 0; attempt < 2; attempt++ {
		if err := history.CopyMigrationHistory("src", "dst"); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if copied, _ := NewMigrationService(nil, nil, root).ListMigrations("dst"); len(copied) != 1 || copied[0].SQL != "select 1" {
		t.Fatalf("copy lists %+v, want exactly the source's one migration", copied)
	}
}

func TestMigrationHistoryCopyRefusesIDsThatLeaveTheStorageRoot(t *testing.T) {
	history := NewMigrationHistoryFiles(t.TempDir())
	for _, ids := range [][2]string{{"../x", "dst"}, {"src", "../x"}, {"", "dst"}} {
		if err := history.CopyMigrationHistory(ids[0], ids[1]); err == nil {
			t.Errorf("CopyMigrationHistory(%q, %q) = nil, want a refusal", ids[0], ids[1])
		}
	}
}

func TestCarryingRestoredSettingsNeedsTheMigrationHistory(t *testing.T) {
	svc := NewProvisioningService(emptyInstanceStore(t), provisioner.NewFactory(), nil)
	svc.SetRestoredSettings(&recordingSettings{})
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); !errors.Is(err, ErrMigrationHistoryNotConfigured) {
		t.Fatalf("CarryRestoredSettings = %v, want ErrMigrationHistoryNotConfigured", err)
	}
}

func TestCarryingRestoredSettingsCopiesTheMigrationHistory(t *testing.T) {
	svc := NewProvisioningService(emptyInstanceStore(t), provisioner.NewFactory(), nil)
	svc.SetRestoredSettings(&recordingSettings{})
	history := &recordingHistory{}
	svc.SetMigrationHistory(history)
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err != nil {
		t.Fatal(err)
	}
	if len(history.copies) != 1 || history.copies[0] != [2]string{"src", "dst"} {
		t.Fatalf("history copies = %v", history.copies)
	}
}

func TestCarryingRestoredSettingsFailsWhenTheHistoryCannotBeCopied(t *testing.T) {
	svc := NewProvisioningService(emptyInstanceStore(t), provisioner.NewFactory(), nil)
	svc.SetRestoredSettings(&recordingSettings{})
	svc.SetMigrationHistory(&recordingHistory{err: errors.New("disk full")})
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err == nil {
		t.Fatal("CarryRestoredSettings = nil, want the copy failure")
	}
}

type recordingHistory struct {
	copies [][2]string
	err    error
}

func (r *recordingHistory) CopyMigrationHistory(sourceID, targetID string) error {
	r.copies = append(r.copies, [2]string{sourceID, targetID})
	return r.err
}
