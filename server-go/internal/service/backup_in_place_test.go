package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// plainAdapter is a BackupAdapter that cannot rebuild a database in place.
type plainAdapter struct{ BackupAdapter }

func backupServiceWith(t *testing.T, adapter BackupAdapter) (*BackupService, *inPlaceWorld) {
	t.Helper()
	instances, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project := inPlaceProject()
	project.DeploymentMode = domain.ModeK8s
	if err := instances.Create(project); err != nil {
		t.Fatal(err)
	}
	world := &inPlaceWorld{instances: instances}
	svc := NewBackupServiceWithAdapters(instances, map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: adapter}, "")
	return svc, world
}

func TestBackupServiceRestoreInPlaceNeedsWiring(t *testing.T) {
	svc, _ := backupServiceWith(t, NewK8sBackupAdapter(k8s.NewMockClient(), nil))
	if err := svc.RestoreInPlace(context.Background(), "proj-a", restoreAt()); !errors.Is(err, ErrInPlaceRestoreNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

func TestBackupServiceRestoreInPlaceNeedsAnAdapterThatCan(t *testing.T) {
	svc, world := backupServiceWith(t, plainAdapter{})
	svc.SetInPlaceRestore(NewInPlaceRestore(InPlaceRestoreConfig{Projects: world, Instances: world.instances, Probe: world}))
	if err := svc.RestoreInPlace(context.Background(), "proj-a", restoreAt()); !errors.Is(err, ErrInPlaceRestoreUnsupported) {
		t.Fatalf("got %v", err)
	}
	if svc.SupportsInPlaceRestore(domain.ModeK8s) {
		t.Error("this adapter cannot restore in place")
	}
}

func TestBackupServiceRestoreInPlaceRunsTheRestore(t *testing.T) {
	adapter := NewK8sBackupAdapter(k8s.NewMockClient(), nil)
	svc, world := backupServiceWith(t, adapter)
	svc.SetInPlaceRestore(NewInPlaceRestore(InPlaceRestoreConfig{Projects: world, Instances: world.instances, Probe: world}))
	world.holdErr = ErrProjectOperationRunning

	if err := svc.RestoreInPlace(context.Background(), "proj-a", restoreAt()); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("the restore must run (and here be refused by the busy project), got %v", err)
	}
	if !svc.SupportsInPlaceRestore(domain.ModeK8s) || svc.SupportsInPlaceRestore(domain.ModeDocker) {
		t.Error("kubernetes can, a mode with no adapter cannot")
	}
}

func TestBackupServiceRestoreInPlaceUnknownProject(t *testing.T) {
	svc, world := backupServiceWith(t, NewK8sBackupAdapter(k8s.NewMockClient(), nil))
	svc.SetInPlaceRestore(NewInPlaceRestore(InPlaceRestoreConfig{Projects: world, Instances: world.instances, Probe: world}))
	if err := svc.RestoreInPlace(context.Background(), "nope", restoreAt()); err == nil {
		t.Fatal("want an error")
	}
}
