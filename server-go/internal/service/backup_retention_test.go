package service

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type countingRetention struct{ passes int }

func (c *countingRetention) PurgeExpiredBackups(context.Context) BackupRetentionReport {
	c.passes++
	return BackupRetentionReport{}
}

// MOCO never deletes a backup, so the platform's sweep runs retention too.
func TestDeletionSweeperRunsBackupRetention(t *testing.T) {
	retention := &countingRetention{}
	sweeper := NewDeletionSweeper(DeletionSweeperConfig{Work: &countingSweep{}, Lock: AlwaysLeader{}, Retention: retention})
	sweeper.tick(context.Background())
	if retention.passes != 1 {
		t.Fatalf("retention passes %d", retention.passes)
	}
	sweeper = NewDeletionSweeper(DeletionSweeperConfig{Work: &countingSweep{}, Lock: neverLeader{}, Retention: retention})
	sweeper.tick(context.Background())
	if retention.passes != 1 {
		t.Fatal("only the leading replica purges")
	}
}

// Retention runs for live MySQL projects with backups; Postgres keeps CNPG's
// own retention and a project being deleted is the teardown's.
func TestPurgeExpiredBackupsCoversLiveMySQLProjects(t *testing.T) {
	lab := newMySQLAdapterLab(t, sourceKeys()...)
	days := 1
	lab.source.BackupRetentionDays = &days
	lab.now = at(7, 0).Add(24 * time.Hour)
	store := emptyInstanceStore(t)
	deleting := *lab.source
	deleting.ProjectID, deleting.Status = "gone", string(domain.StatusDeleting)
	postgres := *lab.source
	postgres.ProjectID, postgres.DBType = "pg", domain.PostgreSQL
	for _, inst := range []*domain.DatabaseInstance{lab.source, &deleting, &postgres} {
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: &fakeAdapter{}}, "")
	svc.RegisterEngineAdapter(domain.ModeK8s, domain.MySQL, lab.adapter)
	report := svc.PurgeExpiredBackups(context.Background())
	if len(report.Failed) != 0 || len(report.Purged) != 1 || report.Purged[0] != mysqlSourceProject || report.Objects != 2 {
		t.Fatalf("report %+v", report)
	}
}
