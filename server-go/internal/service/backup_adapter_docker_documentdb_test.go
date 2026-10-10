package service

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// recordingDocker is an engine whose container ids are their names. It keeps
// every spec and every tar written, in call order.
type recordingDocker struct {
	specs   map[string]provisioner.ContainerSpec
	calls   []string
	copies  []recordedCopy
	removed []string
	// stdin is every statement fed to an exec, with the container it ran in.
	stdin []recordedCopy
	// failGatewayStart makes the gateway's start fail.
	failGatewayStart bool
}

type recordedCopy struct {
	container, dst string
	body           []byte
}

func newRecordingDocker() *recordingDocker {
	return &recordingDocker{specs: map[string]provisioner.ContainerSpec{}}
}

func (r *recordingDocker) CreateContainer(ctx context.Context, name, image string, env, ports map[string]string, limits provisioner.ContainerLimits) (string, error) {
	return r.CreateContainerSpec(ctx, provisioner.ContainerSpec{Name: name, Image: image, Env: env, Ports: ports, Limits: limits})
}
func (r *recordingDocker) CreateContainerSpec(_ context.Context, spec provisioner.ContainerSpec) (string, error) {
	r.calls = append(r.calls, "create:"+spec.Name)
	r.specs[spec.Name] = spec
	return spec.Name, nil
}
func (r *recordingDocker) StartContainer(_ context.Context, id string) error {
	r.calls = append(r.calls, "start:"+id)
	if r.failGatewayStart && strings.HasSuffix(id, "-documentdb") {
		return errors.New("gateway start failed")
	}
	return nil
}
func (r *recordingDocker) StopContainer(_ context.Context, id string) error {
	r.calls = append(r.calls, "stop:"+id)
	return nil
}
func (r *recordingDocker) RemoveContainer(_ context.Context, id string) error {
	r.calls = append(r.calls, "remove:"+id)
	r.removed = append(r.removed, id)
	return nil
}
func (r *recordingDocker) ContainerStatus(context.Context, string) (string, error) {
	return "running", nil
}
func (r *recordingDocker) ContainerState(context.Context, string) (provisioner.ContainerState, error) {
	return provisioner.ContainerState{Found: true, Running: true}, nil
}
func (r *recordingDocker) WaitForHealthy(context.Context, string) error { return nil }
func (r *recordingDocker) ExecInContainer(_ context.Context, id string, cmd []string) (int, error) {
	r.calls = append(r.calls, "exec:"+id+":"+cmd[0])
	return 0, nil
}
func (r *recordingDocker) ExecInContainerStdin(_ context.Context, id string, _ []string, stdin string) (string, error) {
	r.calls = append(r.calls, "stdin:"+id)
	r.stdin = append(r.stdin, recordedCopy{container: id, body: []byte(stdin)})
	return "", nil
}
func (r *recordingDocker) CopyToContainer(_ context.Context, id, dst string, content io.Reader) error {
	body, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	r.calls = append(r.calls, "copy:"+id+":"+dst)
	r.copies = append(r.copies, recordedCopy{container: id, dst: dst, body: body})
	return nil
}
func (r *recordingDocker) CopyFromContainer(_ context.Context, id, src string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("copy from %s:%s not recorded", id, src)
}

func (r *recordingDocker) index(call string) int {
	for i, c := range r.calls {
		if c == call {
			return i
		}
	}
	return -1
}

func restoreDocumentDBSource(t *testing.T, req domain.RestoreRequest) (*recordingDocker, *DockerBackupAdapter, *domain.DatabaseInstance, error) {
	t.Helper()
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	docker := newRecordingDocker()
	adapter.SetDockerClient(docker)
	adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
	src, _ := store.FindByProjectID("dk-1")
	src.DocumentDB = true
	uploader.objects["test-backups/backups/dk-1/manual/backup-2026.tar.gz"] = minimalGzippedTar(t)
	_ = records.Save(context.Background(), &domain.BackupRecord{
		ID: "backup-2026", ProjectID: "dk-1", Status: "COMPLETED", Type: "MANUAL",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	req.NewProjectName, req.TargetProjectID = "dk-restored", "dk-restored"
	_, err := adapter.Restore(context.Background(), src, req)
	restored, _ := store.FindByProjectID("dk-restored")
	return docker, adapter, restored, err
}

func TestDockerAdapterRestoresADocumentDBProjectIntoTheCatalogueImage(t *testing.T) {
	docker, _, restored, err := restoreDocumentDBSource(t, domain.RestoreRequest{})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	database := docker.specs["excalibase-dk-restored-postgres"]
	want, _ := config.DockerDocumentDBImage("17")
	if database.Image != want || database.DataVolume == "" {
		t.Fatalf("database spec %+v", database)
	}
	if restored == nil || !restored.DocumentDB {
		t.Fatalf("restored row %+v must stay a DocumentDB project", restored)
	}
	if len(docker.copies) < 2 || docker.copies[0].dst != "/var/lib/postgresql" || docker.copies[1].dst != provisioner.DocumentDBDataDir {
		t.Fatalf("copies %v", docker.calls)
	}
	header := firstTarHeader(t, docker.copies[0].body)
	if header.Name != "data/" || header.Typeflag != tar.TypeDir || header.Mode != 0o700 ||
		header.Uid != provisioner.DocumentDBImageOwner.UID || header.Gid != provisioner.DocumentDBImageOwner.GID {
		t.Fatalf("data directory entry %+v", header)
	}
}

func TestDockerAdapterRestoredDocumentDBProjectGetsItsGatewayBeforeRegistration(t *testing.T) {
	docker, _, _, err := restoreDocumentDBSource(t, domain.RestoreRequest{})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	gateway, ok := docker.specs["excalibase-dk-restored-documentdb"]
	if !ok || gateway.NetnsOf != "excalibase-dk-restored-postgres" || gateway.Image != config.DocumentDBGatewayImage() {
		t.Fatalf("gateway spec %+v (calls %v)", gateway, docker.calls)
	}
	if docker.index("start:excalibase-dk-restored-postgres") > docker.index("create:excalibase-dk-restored-documentdb") {
		t.Fatalf("gateway created before its database started: %v", docker.calls)
	}
	tier, _ := provisioner.LimitsForTier(enterprisePlanConfig(t))
	database := docker.specs["excalibase-dk-restored-postgres"].Limits
	if database.MemoryBytes+gateway.Limits.MemoryBytes != tier.MemoryBytes {
		t.Fatalf("database %+v + gateway %+v != plan %+v", database, gateway.Limits, tier)
	}
}

func TestDockerAdapterPointInTimeDocumentDBRestoreWritesFilesTheImageUserOwns(t *testing.T) {
	target := domain.ZonedTime{Time: time.Now().Add(-time.Minute)}
	docker, _, _, err := restoreDocumentDBSource(t, domain.RestoreRequest{TargetTime: &target})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	var recovery recordedCopy
	for _, copied := range docker.copies {
		if copied.dst == provisioner.DocumentDBDataDir {
			recovery = copied
		}
	}
	header := firstTarHeader(t, recovery.body)
	if header.Name != "recovery.signal" || header.Uid != provisioner.DocumentDBImageOwner.UID {
		t.Fatalf("recovery files %+v", header)
	}
}

func TestDockerAdapterRemovesTheGatewayThenTheDatabaseWhenARestoreFails(t *testing.T) {
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	docker := newRecordingDocker()
	docker.failGatewayStart = true
	adapter.SetDockerClient(docker)
	adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
	src, _ := store.FindByProjectID("dk-1")
	src.DocumentDB = true
	uploader.objects["test-backups/backups/dk-1/manual/backup-2026.tar.gz"] = minimalGzippedTar(t)
	_ = records.Save(context.Background(), &domain.BackupRecord{ID: "backup-2026", ProjectID: "dk-1", Status: "COMPLETED", Type: "MANUAL",
		Timestamp: time.Now().UTC().Format(time.RFC3339)})
	if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dk-restored", TargetProjectID: "dk-restored"}); err == nil {
		t.Fatal("a restore whose gateway never started must fail")
	}
	gateway := docker.index("remove:excalibase-dk-restored-documentdb")
	database := docker.index("remove:excalibase-dk-restored-postgres")
	if database < 0 || gateway < 0 || gateway > database {
		t.Fatalf("removals %v", docker.removed)
	}
	if got, _ := store.FindByProjectID("dk-restored"); got != nil {
		t.Fatal("a failed restore left a project row")
	}
}

func firstTarHeader(t *testing.T, body []byte) *tar.Header {
	t.Helper()
	header, err := tar.NewReader(bytes.NewReader(body)).Next()
	if err != nil {
		t.Fatalf("read tar: %v", err)
	}
	return header
}

func enterprisePlanConfig(t *testing.T) config.TierConfig {
	t.Helper()
	return enterprisePlan().plan.Config
}
