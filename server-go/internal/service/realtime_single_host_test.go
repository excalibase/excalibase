package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/tenantcert"
)

// A single host runs each Postgres project's CDC watcher as a container
// (EXC-612), started, stopped and restarted where Kubernetes does it.

const singleHostWatcherImage = "docker.io/excalibase/excalibase-watcher-go@sha256:7b2f9feb21b2a92b34c3f7b136399c619d460bf5d296215b8662a7a334126d9e"

func singleHostWatching(t *testing.T) (*registrationHarness, *recordingDocker) {
	t.Helper()
	h := newRegistrationHarness(t)
	docker := newRecordingDocker()
	pg := provisioner.NewDockerPostgreSQLProvisioner(docker)
	pg.SetWatcher(provisioner.DockerWatcherConfig{Image: singleHostWatcherImage, NatsURL: "nats://nats:4222", Network: "excalibase-cdc", Stream: "CDC"})
	h.svc.factory = provisioner.NewFactory(pg)
	return h, docker
}

func singleHostProject() *domain.DatabaseInstance {
	inst := restoredInstance()
	inst.DeploymentMode = domain.ModeDocker
	inst.Namespace = "excalibase-" + inst.ProjectID + "-postgres"
	inst.Host = inst.Namespace
	return inst
}

func singleHostRoleSpec(documentDB bool) projectRoleSpec {
	inst := singleHostProject()
	return projectRoleSpec{projectID: inst.ProjectID, namespace: inst.Namespace, host: inst.Host, port: 5432,
		databaseName: "app", documentDB: documentDB}
}

func TestRegistrationStartsASingleHostProjectsWatcher(t *testing.T) {
	h, docker := singleHostWatching(t)
	pc := provisioner.NewProvisionContext(nil, nil)
	if err := h.svc.deployWatcher(context.Background(), singleHostRoleSpec(false), pc, tenantcert.Material{}, "watcher-pw"); err != nil {
		t.Fatal(err)
	}
	name := provisioner.WatcherContainerName(testRegProject)
	spec, created := docker.specs[name]
	if !created || spec.Image != singleHostWatcherImage || docker.index("start:"+name) < 0 {
		t.Fatalf("no watcher started: %v", docker.calls)
	}
}

func TestASingleHostDocumentDBProjectRunsNoWatcher(t *testing.T) {
	h, docker := singleHostWatching(t)
	pc := provisioner.NewProvisionContext(nil, nil)
	if err := h.svc.deployWatcher(context.Background(), singleHostRoleSpec(true), pc, tenantcert.Material{}, "watcher-pw"); err != nil {
		t.Fatal(err)
	}
	if len(docker.calls) != 0 {
		t.Fatalf("calls %v", docker.calls)
	}
}

func TestResumeRestartsASingleHostWatcherFromItsFiledLogin(t *testing.T) {
	h, docker := singleHostWatching(t)
	inst := singleHostProject()
	record := map[string]string{"host": inst.Host, "port": "5432", "database": "app", "username": roleWatcher, "password": "watcher-pw"}
	if err := h.vault.Put(vaultCredentialPath(inst.ProjectID, roleWatcher), record); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.RestartReplication(context.Background(), inst); err != nil {
		t.Fatalf("RestartReplication: %v", err)
	}
	if docker.index("start:"+provisioner.WatcherContainerName(inst.ProjectID)) < 0 {
		t.Fatalf("watcher not restarted: %v", docker.calls)
	}
}

func TestResumeOfASingleHostProjectWithoutItsWatcherLoginFails(t *testing.T) {
	h, _ := singleHostWatching(t)
	if err := h.svc.RestartReplication(context.Background(), singleHostProject()); err == nil {
		t.Fatal("a resume that cannot restart realtime must say so")
	}
}

func TestStopReplicationStopsASingleHostWatcher(t *testing.T) {
	h, docker := singleHostWatching(t)
	inst := singleHostProject()
	if err := h.svc.StopReplication(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(docker.calls, "stop:"+provisioner.WatcherContainerName(inst.ProjectID)) {
		t.Fatalf("calls %v", docker.calls)
	}
}

func TestARestoredSingleHostProjectDecodesLogically(t *testing.T) {
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	docker := newRecordingDocker()
	adapter.SetDockerClient(docker)
	adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
	src, _ := store.FindByProjectID("dk-1")
	uploader.objects["test-backups/backups/dk-1/manual/backup-2026.tar.gz"] = minimalGzippedTar(t)
	_ = records.Save(context.Background(), &domain.BackupRecord{
		ID: "backup-2026", ProjectID: "dk-1", Status: "COMPLETED", Type: "MANUAL",
		Timestamp: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	})
	if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dk-restored", TargetProjectID: "dk-restored"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	command := docker.specs["excalibase-dk-restored-postgres"].Cmd
	if len(command) < 3 || !slices.Equal(command[:3], []string{"postgres", "-c", "wal_level=logical"}) {
		t.Fatalf("restored postgres command %v", command)
	}
}
