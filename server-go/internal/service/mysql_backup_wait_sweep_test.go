package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type recordingEgressWaiter struct {
	ensured []string
	failOn  string
}

func (w *recordingEgressWaiter) EnsureBackupEgressWait(_ context.Context, namespace, projectID string) (bool, error) {
	w.ensured = append(w.ensured, namespace+"/"+projectID)
	if projectID == w.failOn {
		return false, errors.New("api server down")
	}
	return true, nil
}

func waitSweepProject(projectID string, dbType domain.DatabaseType, mode domain.DeploymentMode, backups bool, status string) *domain.DatabaseInstance {
	return &domain.DatabaseInstance{
		ProjectID: projectID, Namespace: "org1-" + projectID, DBType: dbType, DeploymentMode: mode,
		BackupEnabled: &backups, Status: status,
	}
}

// EXC-586: every running Kubernetes MySQL project with backups gets the wait
// on MOCO's CronJob, so one made before the fix, or remade by MOCO, heals.
func TestMySQLBackupWaitSweep(t *testing.T) {
	store := emptyInstanceStore(t)
	active := string(domain.StatusActive)
	projects := []*domain.DatabaseInstance{
		waitSweepProject("my1", domain.MySQL, domain.ModeK8s, true, active),
		waitSweepProject("broken", domain.MySQL, domain.ModeK8s, true, active),
		waitSweepProject("my2", domain.MySQL, domain.ModeK8s, true, active),
		waitSweepProject("nobackups", domain.MySQL, domain.ModeK8s, false, active),
		waitSweepProject("paused", domain.MySQL, domain.ModeK8s, true, "PAUSED"),
		waitSweepProject("pg", domain.PostgreSQL, domain.ModeK8s, true, active),
		waitSweepProject("host", domain.MySQL, domain.ModeDocker, true, active),
	}
	empty := waitSweepProject("empty", domain.MySQL, domain.ModeK8s, true, active)
	empty.NoDatabase = true
	for _, inst := range append(projects, empty) {
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
	}
	waiter := &recordingEgressWaiter{failOn: "broken"}
	NewMySQLBackupWaitSweep(waiter, store, NewLeadership(AlwaysLeader{})).Sweep(context.Background())
	slices.Sort(waiter.ensured)
	want := []string{"org1-broken/broken", "org1-my1/my1", "org1-my2/my2"}
	if !slices.Equal(waiter.ensured, want) {
		t.Errorf("ensured %v, want %v (a failing project must not stop the rest)", waiter.ensured, want)
	}
}

func TestMySQLBackupWaitSweep_OnlyTheLeader(t *testing.T) {
	store := emptyInstanceStore(t)
	if err := store.Create(waitSweepProject("my1", domain.MySQL, domain.ModeK8s, true, string(domain.StatusActive))); err != nil {
		t.Fatal(err)
	}
	waiter := &recordingEgressWaiter{}
	NewMySQLBackupWaitSweep(waiter, store, NewLeadership(neverLeader{})).Sweep(context.Background())
	NewMySQLBackupWaitSweep(waiter, store, NewLeadership(erroringLock{err: errors.New("down")})).Sweep(context.Background())
	if len(waiter.ensured) != 0 {
		t.Errorf("a follower swept: %v", waiter.ensured)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	NewMySQLBackupWaitSweep(waiter, store, NewLeadership(AlwaysLeader{})).Run(ctx, time.Hour)
	if len(waiter.ensured) != 1 {
		t.Errorf("Run did not sweep once before it stopped: %v", waiter.ensured)
	}
}
