package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fakeRegistrar stands in for the shared registration path so adapter tests
// can assert both adapters route through it, without a vault or a database.
type fakeRegistrar struct {
	calls []*domain.DatabaseInstance
	opts  []RegistrationOptions
	err   error
	// store, when set, persists the row so the caller's verify-then-activate
	// flip has something to update.
	store storage.InstanceStore
}

func (f *fakeRegistrar) RegisterProject(_ context.Context, inst *domain.DatabaseInstance, opts RegistrationOptions) error {
	f.calls = append(f.calls, inst)
	f.opts = append(f.opts, opts)
	if f.err != nil {
		return f.err
	}
	if opts.Unverified {
		markProjectRestoring(inst)
	} else {
		markProjectActive(inst)
	}
	if f.store != nil {
		return f.store.Create(inst)
	}
	return nil
}

// newRestoreReadyAdapter builds a K8s adapter whose recovered cluster reports
// itself healthy on the first poll, so tests exercise the register step
// rather than the wait. The clock is fake: nothing sleeps.
func newRestoreReadyAdapter(t *testing.T, mock *k8s.MockClient, reg ProjectRegistrar) *K8sBackupAdapter {
	t.Helper()
	mock.WildcardPodReady = true
	store := emptyInstanceStore(t)
	if f, ok := reg.(*fakeRegistrar); ok && f.store == nil {
		f.store = store
	}
	adapter := NewK8sBackupAdapter(mock, t.TempDir(), StaticBackupStorage(r2Storage()))
	adapter.SetInstanceStore(store)
	adapter.SetProjectRegistrar(reg)
	adapter.SetDatabaseProbe(alwaysAnswers{})
	adapter.SetReadyPoller(reconcilingPoller(mock))
	return adapter
}

// alwaysAnswers is a DatabaseProbe for tests whose subject is not the probe.
type alwaysAnswers struct{}

func (alwaysAnswers) Probe(context.Context, string) error { return nil }

// reconcilingPoller stands in for the CNPG operator: each tick stamps the
// healthy status onto every Cluster the restore has applied, so the adapter's
// observation succeeds on the next look without any real waiting.
func reconcilingPoller(mock *k8s.MockClient) provisioner.Poller {
	now := time.Unix(0, 0)
	return provisioner.Poller{
		Interval: time.Second,
		Timeout:  time.Minute,
		Now:      func() time.Time { return now },
		After: func(d time.Duration) <-chan time.Time {
			now = now.Add(d)
			for key, obj := range mock.CRDs {
				if !strings.HasSuffix(key, postgresClusterSuffix) {
					continue
				}
				name := key[strings.Index(key, "/")+1:]
				_ = unstructured.SetNestedMap(obj.Object, map[string]interface{}{
					"phase":          "Cluster in healthy state",
					"currentPrimary": name + "-1",
					"readyInstances": int64(1),
				}, "status")
			}
			ch := make(chan time.Time, 1)
			ch <- now
			return ch
		},
	}
}

func TestK8sRestoreRegistersTheRestoredProject(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)

	resp, err := adapter.Restore(context.Background(), sourceInstance(),
		domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst", BackupID: "bk-9"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(reg.calls) != 1 {
		t.Fatalf("restore must register the new project exactly once, got %d", len(reg.calls))
	}
	got := reg.calls[0]
	if got.ProjectID != "dst" || got.OrgID != "org" || got.Namespace != "org-dst" {
		t.Errorf("registered instance: %+v", got)
	}
	if got.RestoredFromProjectID != "src" || got.RestoredFromBackupID != "bk-9" {
		t.Errorf("restore provenance: %+v", got)
	}
	if !reg.opts[0].ResetRolePasswords {
		t.Error("a restored cluster carries the source's role passwords; they must be reset")
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("response status: %q", resp.Status)
	}
}

// emptyInstanceStore is a store with no projects — a restore target id is
// always free in it, so tests exercise the path past the collision check.
func emptyInstanceStore(t *testing.T) storage.InstanceStore {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	return store
}

func TestK8sRestoreRefusesWithoutRegistrar(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := NewK8sBackupAdapter(mock, t.TempDir(), StaticBackupStorage(r2Storage()))
	adapter.SetInstanceStore(emptyInstanceStore(t))

	_, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrProjectRegistrarNotConfigured) {
		t.Fatalf("err: got %v, want ErrProjectRegistrarNotConfigured", err)
	}
	if len(mock.CRDs) != 0 {
		t.Error("nothing may be created when the restore could not be registered")
	}
}

func TestK8sRestoreFailsWhenRegistrationFails(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{err: errors.New("vault sealed")}
	adapter := newRestoreReadyAdapter(t, mock, reg)

	_, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("err: got %v, want ErrRestoreNotObserved", err)
	}
	if mock.Namespaces["org-dst"] {
		t.Error("a restore that could not be registered must compensate its namespace away")
	}
}

func TestK8sRestoreFailsWhenPrimaryNeverReady(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)
	mock.WildcardPodReady = false

	_, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if err == nil {
		t.Fatal("restore must fail when the recovered primary never becomes ready")
	}
	if len(reg.calls) != 0 {
		t.Error("nothing may be registered before the restored database is up")
	}
}

// dockerRestoreHarness is a Docker adapter with a source project, a stored
// base backup and a fake docker client — everything a Restore needs except a
// registrar, which each test wires itself.
type dockerRestoreHarness struct {
	adapter   *DockerBackupAdapter
	instances *storage.FileSystemStore
	source    *domain.DatabaseInstance
}

func newDockerRestoreHarness(t *testing.T) *dockerRestoreHarness {
	t.Helper()
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{})
	uploader.objects["test-backups/backups/dk-1/manual/backup-reg.tar.gz"] = minimalGzippedTar(t)
	if err := records.Save(context.Background(), &domain.BackupRecord{
		ID: "backup-reg", ProjectID: "dk-1", Status: "COMPLETED", Type: "MANUAL",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed backup record: %v", err)
	}
	src, _ := store.FindByProjectID("dk-1")
	return &dockerRestoreHarness{adapter: adapter, instances: store, source: src}
}

func TestDockerRestoreRegistersInsteadOfSavingTheRowItself(t *testing.T) {
	h := newDockerRestoreHarness(t)
	reg := &fakeRegistrar{store: h.instances}
	h.adapter.SetProjectRegistrar(reg)

	resp, err := h.adapter.Restore(context.Background(), h.source, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(reg.calls) != 1 {
		t.Fatalf("restore must register the new project exactly once, got %d", len(reg.calls))
	}
	got := reg.calls[0]
	if got.ProjectID != "dst" || got.DeploymentMode != domain.ModeDocker {
		t.Errorf("registered instance: %+v", got)
	}
	if got.RestoredFromProjectID != h.source.ProjectID || got.RestoredFromBackupID == "" {
		t.Errorf("restore provenance: %+v", got)
	}
	if !reg.opts[0].ResetRolePasswords || !reg.opts[0].ResetAdminPassword {
		t.Errorf("a seeded data directory keeps the source passwords; both resets are required: %+v", reg.opts[0])
	}
	// The only row that exists is the one registration wrote, flipped to
	// ACTIVE after the probe — the adapter never persists one itself.
	saved, _ := h.instances.FindByProjectID("dst")
	if saved == nil || saved.Status != "ACTIVE" {
		t.Errorf("registered row: %+v", saved)
	}
	if resp.Status != "ACTIVE" {
		t.Errorf("response status: %q", resp.Status)
	}
}

func TestDockerRestoreFailsWhenRegistrationFails(t *testing.T) {
	h := newDockerRestoreHarness(t)
	h.adapter.SetProjectRegistrar(&fakeRegistrar{err: errors.New("vault sealed")})

	if _, err := h.adapter.Restore(context.Background(), h.source, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err == nil {
		t.Fatal("registration failure must fail the restore so the job is marked FAILED")
	}
	if saved, _ := h.instances.FindByProjectID("dst"); saved != nil {
		t.Error("no project row may survive a failed registration")
	}
}

func TestDockerRestoreRefusesWithoutRegistrar(t *testing.T) {
	h := newDockerRestoreHarness(t)

	_, err := h.adapter.Restore(context.Background(), h.source, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrProjectRegistrarNotConfigured) {
		t.Fatalf("err: got %v, want ErrProjectRegistrarNotConfigured", err)
	}
}

// armRestore wires onto a BackupService everything a verified restore needs
// from the platform: a database that answers, and a stand-in operator that
// reconciles the recovered cluster on the first poll.
func armRestore(svc *BackupService, mock *k8s.MockClient, store storage.InstanceStore) {
	_ = svc.SetDatabaseProbe(alwaysAnswers{})
	for _, adapter := range svc.adapters {
		if k8sAdapter, ok := adapter.(*K8sBackupAdapter); ok {
			k8sAdapter.SetReadyPoller(reconcilingPoller(mock))
			k8sAdapter.SetInstanceStore(store)
		}
	}
}
