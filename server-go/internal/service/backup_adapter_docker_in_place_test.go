package service

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// heldPorts is the host-port allocator a single host's restores see.
type heldPorts struct {
	held     map[string]int
	released []string
}

func (h *heldPorts) Allocate(_ context.Context, projectID string) (int, error) {
	if port, ok := h.held[projectID]; ok {
		return port, nil
	}
	h.held[projectID] = 30000 + len(h.held) + 1
	return h.held[projectID], nil
}

func (h *heldPorts) Release(_ context.Context, projectID string) error {
	delete(h.held, projectID)
	h.released = append(h.released, projectID)
	return nil
}

type inPlaceFixture struct {
	adapter *DockerBackupAdapter
	dc      *fakeDockerClientForAdapter
	ports   *heldPorts
	inst    *domain.DatabaseInstance
	records *fakeBackupRecordStore
}

func newInPlaceFixture(t *testing.T) inPlaceFixture {
	t.Helper()
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	dc := &fakeDockerClientForAdapter{walArchive: map[string][]byte{}}
	adapter.SetDockerClient(dc)
	ports := &heldPorts{held: map[string]int{"dk-1": 30007}}
	adapter.SetHostPorts(ports)
	inst, _ := store.FindByProjectID("dk-1")
	inst.BackupEnabled = boolPtr(true)
	_ = store.Update(inst)
	seedDockerBackups(t, uploader, records, map[string]time.Time{"backup-old": time.Now().Add(-2 * time.Hour)})
	return inPlaceFixture{adapter: adapter, dc: dc, ports: ports, inst: inst, records: records}
}

// EXC-593: an in-place restore rebuilds the project's own container from its
// backups: same name, same host port, archiving again, and the project's row
// names the new container.
func TestDockerInPlaceRestoreRebuildsTheProjectsOwnContainer(t *testing.T) {
	f := newInPlaceFixture(t)
	ctx := context.Background()
	point := time.Now().Add(-time.Hour)
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: point}})
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	safety, err := f.adapter.TakeSafetyBackup(ctx, f.inst)
	if err != nil || safety == "" {
		t.Fatalf("safety backup %q: %v", safety, err)
	}
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !slices.Contains(f.dc.removed, "excalibase-dk-1-postgres") || f.dc.createdName != "excalibase-dk-1-postgres" {
		t.Fatalf("removed %v, created %q", f.dc.removed, f.dc.createdName)
	}
	if f.dc.ports["5432"] != "30007" || len(f.ports.released) != 0 {
		t.Fatalf("published %v, released %v", f.dc.ports, f.ports.released)
	}
	row, _ := f.adapter.instances.FindByProjectID("dk-1")
	if f.inst.Namespace != "container-excalibase-dk-1-postgres" || row.Namespace != f.inst.Namespace {
		t.Fatalf("namespace %q, row %q", f.inst.Namespace, row.Namespace)
	}
	archived := false
	for _, cmd := range f.dc.execs {
		archived = archived || strings.Contains(strings.Join(cmd, " "), "archive_command")
	}
	if !archived {
		t.Fatal("the rebuilt database does not archive")
	}
}

func TestDockerInPlaceRestoreRefusesWhatItCannotReach(t *testing.T) {
	f := newInPlaceFixture(t)
	ctx := context.Background()
	early := domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: time.Now().Add(-5 * time.Hour)}}
	var target *RestoreTargetError
	if _, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, early); !errors.As(err, &target) {
		t.Fatalf("a point before every backup: %v", err)
	}
	f.inst.DocumentDB = true
	if _, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{}); !errors.Is(err, ErrInPlaceRestoreUnsupported) {
		t.Fatalf("DocumentDB in place: %v", err)
	}
}

// A copy holds a port of its own; one that never came up gives it back.
func TestDockerRestorePublishesTheCopyOnItsOwnPort(t *testing.T) {
	f := newInPlaceFixture(t)
	f.adapter.SetProjectRegistrar(&fakeRegistrar{store: f.adapter.instances})
	if _, err := f.adapter.Restore(context.Background(), f.inst, domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "copy"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if want := strconv.Itoa(f.ports.held["copy"]); f.dc.ports["5432"] != want || want == "0" {
		t.Fatalf("copy published on %v", f.dc.ports)
	}
	f.dc.failOn = "health"
	_, _ = f.adapter.Restore(context.Background(), f.inst, domain.RestoreRequest{NewProjectName: "copy2", TargetProjectID: "copy2"})
	if !slices.Contains(f.ports.released, "copy2") {
		t.Fatalf("a failed copy kept its port: %v", f.ports.released)
	}
}

// A point the project's backups cannot reach is refused before a job is filed.
func TestDockerRestoreTargetIsCheckedBeforeAJob(t *testing.T) {
	f := newInPlaceFixture(t)
	ctx := context.Background()
	at := func(offset time.Duration) domain.RestoreRequest {
		return domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: time.Now().Add(offset)}}
	}
	var target *RestoreTargetError
	for name, req := range map[string]domain.RestoreRequest{"before every backup": at(-5 * time.Hour), "in the future": at(time.Hour)} {
		if err := f.adapter.CheckRestoreTarget(ctx, f.inst, req); !errors.As(err, &target) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := f.adapter.CheckRestoreTarget(ctx, f.inst, at(-time.Hour)); err != nil {
		t.Errorf("within the window: %v", err)
	}
	if err := f.adapter.CheckRestoreTarget(ctx, f.inst, domain.RestoreRequest{}); err != nil {
		t.Errorf("latest: %v", err)
	}
}
