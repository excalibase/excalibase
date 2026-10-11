package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

func dispatchService(t *testing.T) (*BackupService, *storage.FileSystemStore) {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enabled := true
	for _, inst := range []*domain.DatabaseInstance{
		{ProjectID: "pg", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeK8s, BackupEnabled: &enabled},
		{ProjectID: "my", DBType: domain.MySQL, DeploymentMode: domain.ModeK8s, BackupEnabled: &enabled},
	} {
		if err := store.Create(inst); err != nil {
			t.Fatal(err)
		}
	}
	return NewBackupServiceWithAdapters(store, map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: &fakeAdapter{}}, ""), store
}

// ADR 0041 decision 6: the adapter is keyed by (deploy mode, engine). A MySQL
// project never lands on the CNPG adapter.
func TestBackupDispatchIsKeyedByEngine(t *testing.T) {
	svc, _ := dispatchService(t)
	if _, err := svc.TriggerManualBackup(context.Background(), "my"); !errors.Is(err, ErrUnsupportedBackupMode) {
		t.Fatalf("a MySQL project without a MySQL adapter: %v", err)
	}
	mysql := &fakeAdapter{}
	svc.RegisterEngineAdapter(domain.ModeK8s, domain.MySQL, mysql)
	if _, err := svc.TriggerManualBackup(context.Background(), "my"); err != nil {
		t.Fatalf("TriggerManualBackup: %v", err)
	}
	if mysql.triggerCalls != 1 {
		t.Errorf("the MySQL adapter took %d triggers", mysql.triggerCalls)
	}
	postgres := svc.adapters[domain.ModeK8s].(*fakeAdapter)
	if _, err := svc.TriggerManualBackup(context.Background(), "pg"); err != nil || postgres.triggerCalls != 1 {
		t.Errorf("Postgres trigger: %v, %d calls", err, postgres.triggerCalls)
	}
}

type probeTakingAdapter struct {
	fakeAdapter
	probe DatabaseProbe
}

func (a *probeTakingAdapter) SetDatabaseProbe(p DatabaseProbe) { a.probe = p }

type noopProbe struct{}

func (noopProbe) Probe(context.Context, string) error { return nil }

// Every adapter, engine-keyed ones included, must verify a restore.
func TestSetDatabaseProbeReachesEngineAdapters(t *testing.T) {
	svc, _ := dispatchService(t)
	svc.adapters[domain.ModeK8s] = &probeTakingAdapter{}
	svc.RegisterEngineAdapter(domain.ModeK8s, domain.MySQL, &fakeAdapter{})
	if err := svc.SetDatabaseProbe(noopProbe{}); !errors.Is(err, ErrDatabaseProbeNotConfigured) {
		t.Fatalf("an engine adapter without a probe setter: %v", err)
	}
	mysql := &probeTakingAdapter{}
	svc.RegisterEngineAdapter(domain.ModeK8s, domain.MySQL, mysql)
	if err := svc.SetDatabaseProbe(noopProbe{}); err != nil || mysql.probe == nil {
		t.Fatalf("SetDatabaseProbe: %v, probe %v", err, mysql.probe)
	}
}

func TestSupportsInPlaceRestoreForAProject(t *testing.T) {
	svc, store := dispatchService(t)
	my, _ := store.FindByProjectID("my")
	if svc.SupportsInPlaceRestoreFor(my) {
		t.Error("in place without a MySQL adapter")
	}
}

type resolvingAdapter struct {
	fakeAdapter
	window *RestoreWindow
	target error
}

func (a *resolvingAdapter) BackupStatusOf(_ context.Context, _ *domain.DatabaseInstance, id string) (string, error) {
	if id == "job-1" {
		return backupStatusCompleted, nil
	}
	return "", errors.New("unknown")
}

func (a *resolvingAdapter) RestoreWindow(context.Context, *domain.DatabaseInstance) (*RestoreWindow, error) {
	return a.window, nil
}

func (a *resolvingAdapter) CheckRestoreTarget(context.Context, *domain.DatabaseInstance, domain.RestoreRequest) error {
	return a.target
}

// A backup an adapter names by something other than its listing (a MOCO
// Job) is still followed to completion, which the pre-pause backup needs.
func TestBackupStatusAsksAnAdapterThatResolvesItsOwnIDs(t *testing.T) {
	svc, store := dispatchService(t)
	svc.RegisterEngineAdapter(domain.ModeK8s, domain.MySQL, &resolvingAdapter{target: &RestoreTargetError{"outside"}})
	status, err := svc.BackupStatus(context.Background(), "my", "job-1")
	if err != nil || status != backupStatusCompleted {
		t.Fatalf("status %q %v", status, err)
	}
	my, _ := store.FindByProjectID("my")
	if err := svc.CheckRestoreTarget(context.Background(), my, domain.RestoreRequest{}); !errors.Is(err, ErrRestoreTargetInvalid) {
		t.Errorf("CheckRestoreTarget: %v", err)
	}
	pg, _ := store.FindByProjectID("pg")
	if err := svc.CheckRestoreTarget(context.Background(), pg, domain.RestoreRequest{}); err != nil {
		t.Errorf("Postgres targets are checked where they always were: %v", err)
	}
	if window, err := svc.RestoreWindow(context.Background(), "pg"); err != nil || window != nil {
		t.Errorf("Postgres window %v %v", window, err)
	}
}
