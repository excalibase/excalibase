package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// ADR 0041: a MySQL project is paused and resumed by the MySQL provisioner,
// never by the Postgres one registered for its deployment mode.
func TestPauseChoosesThePauserByEngine(t *testing.T) {
	store, _ := storage.NewFileSystemStore(t.TempDir())
	postgres, mysql := &fakePauser{}, &fakePauser{}
	svc := NewPauseService(PauseServiceConfig{
		Instances:     store,
		Pausers:       map[domain.DeploymentMode]provisioner.Pauser{domain.ModeK8s: postgres},
		EnginePausers: map[domain.DatabaseType]provisioner.Pauser{domain.MySQL: mysql},
	})
	inst := &domain.DatabaseInstance{ProjectID: "proj-my", OrgID: "org1", DBType: domain.MySQL,
		DeploymentMode: domain.ModeK8s, Namespace: "org1-proj-my", Status: "ACTIVE"}
	if err := store.Create(inst); err != nil {
		t.Fatal(err)
	}
	if err := svc.Pause(context.Background(), "proj-my", "manual"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := svc.Resume(context.Background(), "proj-my"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if mysql.pauseCalls != 1 || mysql.resumeCalls != 1 || postgres.pauseCalls+postgres.resumeCalls != 0 {
		t.Errorf("mysql %d/%d, postgres %d/%d", mysql.pauseCalls, mysql.resumeCalls, postgres.pauseCalls, postgres.resumeCalls)
	}
}

// An engine with no pauser is refused, not handed to another engine's.
func TestPauseRefusesAnEngineWithoutAPauser(t *testing.T) {
	store, _ := storage.NewFileSystemStore(t.TempDir())
	svc := NewPauseService(PauseServiceConfig{
		Instances: store,
		Pausers:   map[domain.DeploymentMode]provisioner.Pauser{domain.ModeK8s: &fakePauser{}},
	})
	inst := &domain.DatabaseInstance{ProjectID: "proj-my", OrgID: "org1", DBType: domain.MySQL,
		DeploymentMode: domain.ModeK8s, Namespace: "org1-proj-my", Status: "ACTIVE"}
	_ = store.Create(inst)
	if err := svc.Pause(context.Background(), "proj-my", "manual"); err != ErrPauseUnsupported {
		t.Errorf("err = %v", err)
	}
}
