package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// A MySQL project's backups are under MOCO's prefix; purging the project
// clears that prefix and nothing of its neighbours'.
func TestPurgeClearsAMySQLProjectsMOCOPrefix(t *testing.T) {
	deleter := newFakeObjectDeleter(
		"moco/org1-proj-1/proj-1-mysql/20261011-000000/dump.tar",
		"moco/org1-proj-10/proj-10-mysql/20261011-000000/dump.tar",
		"proj-1/cloud/base/keep",
	)
	inst := &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org1", DBType: domain.MySQL, DeploymentMode: domain.ModeK8s}
	deleted, err := newTestPurger(deleter).Purge(context.Background(), inst)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted %d, %v", deleted, err)
	}
	if got := deleter.remaining(); len(got) != 2 {
		t.Errorf("remaining %v", got)
	}
	inst.OrgID = ""
	if _, err := newTestPurger(deleter).Purge(context.Background(), inst); !errors.Is(err, ErrBackupPrefixUnsafe) {
		t.Errorf("a MySQL project without its org: %v", err)
	}
}

// A deleted project's record has no engine; its kept backups are cleared
// from every engine's prefix it could have written.
func TestPurgeRetainedClearsEveryEnginesPrefix(t *testing.T) {
	deleter := newFakeObjectDeleter(
		"moco/org1-proj-1/proj-1-mysql/20261011-000000/dump.tar",
		"proj-1/cloud/base/1",
		"proj-2/cloud/base/keep",
	)
	deleted, err := newTestPurger(deleter).PurgeRetained(context.Background(), storage.RetainedBackup{
		ProjectID: "proj-1", OrgID: "org1", DeploymentMode: domain.ModeK8s,
	})
	if err != nil || deleted != 2 {
		t.Fatalf("deleted %d, %v", deleted, err)
	}
	if got := deleter.remaining(); len(got) != 1 || got[0] != "proj-2/cloud/base/keep" {
		t.Errorf("remaining %v", got)
	}
}

func TestMOCOPrefixValidation(t *testing.T) {
	for _, org := range []string{"", "a/b", "..", " "} {
		if _, err := mocoProjectPrefix(org, "proj-1"); !errors.Is(err, ErrBackupPrefixUnsafe) {
			t.Errorf("org %q: %v", org, err)
		}
	}
	if prefix, err := mocoProjectPrefix("org1", "proj-1"); err != nil || prefix != "moco/org1-proj-1/proj-1-mysql/" {
		t.Errorf("prefix %q %v", prefix, err)
	}
}

// The delete response names where a MySQL project's kept backups are.
func TestRetainedBackupPrefixOfAMySQLProject(t *testing.T) {
	svc, _, _ := setupMySQLTest(t)
	svc.backupPurger = newTestPurger(newFakeObjectDeleter())
	prefix, ok := svc.RetainedBackupPrefix(&domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org1", DBType: domain.MySQL, DeploymentMode: domain.ModeK8s})
	if !ok || prefix != "moco/org1-proj-1/proj-1-mysql/" {
		t.Errorf("prefix %q %v", prefix, ok)
	}
}

// EXC-586: when a project's backups go, so do its backup records, and only
// once the objects are gone.
func TestPurgeDropsTheProjectsBackupRecords(t *testing.T) {
	ctx := context.Background()
	inst := &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org1", DBType: domain.MySQL, DeploymentMode: domain.ModeK8s}
	purges := map[string]func(*BackupPurger) error{
		"purge": func(p *BackupPurger) error { _, err := p.Purge(ctx, inst); return err },
		"retained": func(p *BackupPurger) error {
			_, err := p.PurgeRetained(ctx, storage.RetainedBackup{ProjectID: "proj-1", OrgID: "org1", DeploymentMode: domain.ModeK8s})
			return err
		},
	}
	for name, purge := range purges {
		t.Run(name, func(t *testing.T) {
			records := &fakeBackupRecordStore{}
			_ = records.Save(ctx, &domain.BackupRecord{ID: "a", ProjectID: "proj-1", Type: backupTypeManual})
			_ = records.Save(ctx, &domain.BackupRecord{ID: "b", ProjectID: "proj-10", Type: backupTypeManual})
			deleter := newFakeObjectDeleter("moco/org1-proj-1/proj-1-mysql/20261011-000000/dump.tar")
			deleter.deleteErr = errors.New("store down")
			purger := newTestPurger(deleter)
			purger.SetRecords(records)
			if err := purge(purger); err == nil {
				t.Fatal("no error from a failed purge")
			}
			if len(records.records) != 2 {
				t.Fatalf("records went before the backups: %+v", records.records)
			}
			deleter.deleteErr = nil
			if err := purge(purger); err != nil {
				t.Fatalf("purge: %v", err)
			}
			if len(records.records) != 1 || records.records[0].ProjectID != "proj-10" {
				t.Errorf("records left %+v", records.records)
			}
		})
	}
}
