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
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// upgradeEngine is a container engine with one project's Postgres on it. The
// new container's first start ends as load says: "loaded", "failed", or ""
// (it never ends).
type upgradeEngine struct {
	byID       map[string]*engineContainer
	nextID     int
	load       string
	fail       map[string]error
	extensions []string
	calls      []string
	copied     map[string][]string // container id -> destinations
	// after runs once a named step has run.
	after map[string]func()
	// onCall sees every call as it is made.
	onCall func(call string)
	// notReady keeps an image's server from listening for that many checks;
	// a negative count is never.
	notReady map[string]int
}

func (e *upgradeEngine) note(call string) {
	e.calls = append(e.calls, call)
	if e.onCall != nil {
		e.onCall(call)
	}
}

// listening reports whether c's server takes TCP connections now.
func (e *upgradeEngine) listening(c *engineContainer) bool {
	left, ok := e.notReady[c.image]
	if !ok || left == 0 {
		return true
	}
	if left > 0 {
		e.notReady[c.image] = left - 1
	}
	return false
}

type engineContainer struct {
	id, name, image string
	running, fenced bool
	env             map[string]string
	cmd             []string
}

func newUpgradeEngine() *upgradeEngine {
	engine := &upgradeEngine{byID: map[string]*engineContainer{}, load: provisioner.UpgradeLoaded,
		fail: map[string]error{}, notReady: map[string]int{}, extensions: []string{"plpgsql", "vector", "citext"}, copied: map[string][]string{}}
	engine.byID["old-id"] = &engineContainer{id: "old-id", name: provisioner.DatabaseContainerName("dk-1"), image: "postgres:16", running: true}
	return engine
}

func (e *upgradeEngine) find(ref string) *engineContainer {
	if c, ok := e.byID[ref]; ok {
		return c
	}
	for _, c := range e.byID {
		if c.name == ref {
			return c
		}
	}
	return nil
}

func (e *upgradeEngine) step(name string) error {
	e.note(name)
	if hook := e.after[name]; hook != nil {
		hook()
	}
	return e.fail[name]
}

func (e *upgradeEngine) CreateContainer(ctx context.Context, name, image string, env, ports map[string]string, limits provisioner.ContainerLimits) (string, error) {
	return e.CreateContainerSpec(ctx, provisioner.ContainerSpec{Name: name, Image: image, Env: env, Ports: ports, Limits: limits})
}

func (e *upgradeEngine) CreateContainerSpec(_ context.Context, spec provisioner.ContainerSpec) (string, error) {
	key := "create:" + spec.Image
	if strings.Contains(spec.Name, "-extensions-") {
		key = "scratch:" + spec.Image
	}
	if err := e.step(key); err != nil {
		return "", err
	}
	if e.find(spec.Name) != nil {
		return "", fmt.Errorf("the name %s is in use", spec.Name)
	}
	e.nextID++
	id := fmt.Sprintf("new-id-%d", e.nextID)
	e.byID[id] = &engineContainer{id: id, name: spec.Name, image: spec.Image, env: spec.Env, cmd: spec.Cmd}
	return id, nil
}

func (e *upgradeEngine) StartContainer(_ context.Context, id string) error {
	c := e.find(id)
	if c == nil {
		return errors.New("no such container")
	}
	if err := e.step("start:" + c.image); err != nil {
		return err
	}
	c.running = true
	return nil
}

func (e *upgradeEngine) StopContainer(_ context.Context, id string) error {
	if err := e.step("stop"); err != nil {
		return err
	}
	if c := e.find(id); c != nil {
		c.running = false
	}
	return nil
}

func (e *upgradeEngine) RemoveContainer(_ context.Context, id string) error {
	e.note("remove:" + id)
	if c := e.find(id); c != nil {
		delete(e.byID, c.id)
	}
	return nil
}

func (e *upgradeEngine) RenameContainer(_ context.Context, id, name string) error {
	if err := e.step("rename:" + name); err != nil {
		return err
	}
	c := e.find(id)
	if c == nil {
		return errors.New("no such container")
	}
	if other := e.find(name); other != nil && other != c {
		return fmt.Errorf("the name %s is in use", name)
	}
	c.name = name
	return nil
}

func (e *upgradeEngine) ContainerStatus(_ context.Context, id string) (string, error) {
	if c := e.find(id); c != nil && c.running {
		return "running", nil
	}
	return "stopped", nil
}

func (e *upgradeEngine) WaitForHealthy(context.Context, string) error { return nil }

func (e *upgradeEngine) ContainerState(_ context.Context, id string) (provisioner.ContainerState, error) {
	c := e.find(id)
	if c == nil {
		return provisioner.ContainerState{}, nil
	}
	return provisioner.ContainerState{ID: c.id, Found: true, Running: c.running}, nil
}

func (e *upgradeEngine) loaded(c *engineContainer) bool {
	return c.image == "postgres:16" || e.load == provisioner.UpgradeLoaded
}

func (e *upgradeEngine) ExecInContainer(_ context.Context, id string, cmd []string) (int, error) {
	c := e.find(id)
	if c == nil || !c.running {
		return 1, errors.New("not running")
	}
	joined := strings.Join(cmd, " ")
	e.note("exec:" + c.image + ":" + joined)
	if strings.Contains(joined, "pg_isready") && ((!e.loaded(c) && e.load == "") || !e.listening(c)) {
		return 2, nil
	}
	return 0, nil
}

func (e *upgradeEngine) ExecInContainerStdin(_ context.Context, id string, cmd []string, stdin string) (string, error) {
	c := e.find(id)
	if c == nil || !c.running {
		return "", errors.New("not running")
	}
	switch {
	case strings.Contains(stdin, "pg_prepared_xacts"):
		return "ext\x1fapp\x1fplpgsql\next\x1fapp\x1fvector\n" + preflightDoneMarker + "\n", e.step("preflight")
	case stdin == provisioner.FenceScript():
		if err := e.step("fence:" + c.image); err != nil {
			return "", err
		}
		c.fenced = true
	case stdin == provisioner.UnfenceScript():
		e.note("unfence:" + c.image)
		c.fenced = false
	case stdin == provisioner.DumpScript():
		return "", e.step("dump")
	case stdin == provisioner.UpgradeStateScript():
		return e.load + "\n", nil
	case stdin == provisioner.DataMajorScript():
		return strings.TrimPrefix(c.image, "postgres:") + "\n", nil
	case stdin == provisioner.CleanupScript():
		e.note("cleanup:" + c.image)
	case stdin == afterUpgradeScript:
		return "", e.step("after")
	}
	return "", nil
}

func (e *upgradeEngine) CopyToContainer(_ context.Context, id, dst string, content io.Reader) error {
	_, _ = io.Copy(io.Discard, content)
	e.copied[id] = append(e.copied[id], dst)
	return e.step("copy-to:" + dst)
}

func (e *upgradeEngine) CopyFromContainer(_ context.Context, id, src string) (io.ReadCloser, error) {
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	switch {
	case strings.HasSuffix(src, "/extension"):
		for _, name := range e.extensions {
			_ = writer.WriteHeader(&tar.Header{Name: "extension/" + name + ".control", Typeflag: tar.TypeReg, Mode: 0o644})
		}
	case src == provisioner.UpgradeDir:
		body := []byte("-- dump")
		_ = writer.WriteHeader(&tar.Header{Name: "excalibase-upgrade/dump.sql", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = writer.Write(body)
	}
	_ = writer.Close()
	return io.NopCloser(&buf), nil
}

type dockerUpgradeWorld struct {
	engine   *upgradeEngine
	upgrader *DockerMajorUpgrader
	store    *storage.FileSystemStore
	inst     *domain.DatabaseInstance
	progress *recordingProgress
	now      time.Time
}

func newDockerUpgradeWorld(t *testing.T) *dockerUpgradeWorld {
	t.Helper()
	config.PublishPostgresCatalogForTest()
	adapter, store, _, _, _ := setupDockerAdapter(t)
	engine := newUpgradeEngine()
	adapter.SetDockerClient(engine)
	adapter.SetInstanceStore(store)
	row, _ := store.FindByProjectID("dk-1")
	row.Namespace = "old-id"
	row.PostgresVersion = "16"
	row.BackupEnabled = boolPtr(true)
	_ = store.Update(row)
	inst, _ := store.FindByProjectID("dk-1")
	w := &dockerUpgradeWorld{engine: engine, store: store, inst: inst, progress: &recordingProgress{},
		now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}
	w.upgrader = NewDockerMajorUpgrader(DockerMajorUpgraderConfig{
		Backups: adapter,
		Now:     func() time.Time { return w.now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			w.now = w.now.Add(d)
			return ctx.Err()
		},
	})
	return w
}

func (w *dockerUpgradeWorld) run() domain.MajorUpgrade {
	return domain.MajorUpgrade{ID: "mu-1", ProjectID: "dk-1", FromVersion: "16", ToVersion: "17", Status: domain.MajorUpgradeRunning}
}

func (w *dockerUpgradeWorld) recorded() string {
	row, _ := w.store.FindByProjectID("dk-1")
	return row.Namespace
}

func (w *dockerUpgradeWorld) called(prefix string) bool {
	for _, call := range w.engine.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

// the old server is where it was, serving, open to the network.
func (w *dockerUpgradeWorld) assertPutBack(t *testing.T) {
	t.Helper()
	old := w.engine.byID["old-id"]
	if old == nil || old.name != provisioner.DatabaseContainerName("dk-1") || !old.running || old.fenced {
		t.Fatalf("old container = %+v", old)
	}
	if len(w.engine.byID) != 1 {
		t.Fatalf("containers left: %v", w.engine.byID)
	}
	if w.recorded() != "old-id" {
		t.Fatalf("project records %q", w.recorded())
	}
}

func TestDockerConvertLoadsTheDumpIntoANewContainer(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Converted {
		t.Fatalf("outcome = %+v (calls %v)", outcome, w.engine.calls)
	}
	fresh := w.engine.find(provisioner.DatabaseContainerName("dk-1"))
	if fresh == nil || fresh.id == "old-id" || fresh.image != "postgres:17" || !fresh.running {
		t.Fatalf("new container = %+v", fresh)
	}
	if fresh.env["POSTGRES_DB"] != "postgres" || len(fresh.cmd) == 0 || fresh.cmd[0] != "postgres" {
		t.Fatalf("new container env %v cmd %v", fresh.env, fresh.cmd)
	}
	copied := strings.Join(w.engine.copied[fresh.id], ",")
	for _, want := range []string{"/", "/tmp", provisioner.UpgradeInitDir} {
		if !strings.Contains(","+copied+",", ","+want+",") {
			t.Errorf("not copied into the new container: %s (got %s)", want, copied)
		}
	}
	old := w.engine.find(provisioner.PreUpgradeContainerName("dk-1", "mu-1"))
	if old == nil || old.id != "old-id" || old.running {
		t.Fatalf("old container = %+v", old)
	}
	if w.recorded() != fresh.id || w.inst.Namespace != fresh.id {
		t.Fatalf("project records %q, inst %q", w.recorded(), w.inst.Namespace)
	}
	if w.progress.stopped == nil || w.progress.converted == nil {
		t.Fatalf("progress = %+v", w.progress)
	}
	if !w.called("exec:postgres:16:psql -U postgres -d app -c SELECT pg_switch_wal();") {
		t.Fatalf("the old server's last WAL was not shipped: %v", w.engine.calls)
	}
}

func TestDockerConvertPutsTheOldServerBackWhenTheLoadFails(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.load = provisioner.UpgradeFailed
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.RolledBack || outcome.Converted {
		t.Fatalf("outcome = %+v", outcome)
	}
	w.assertPutBack(t)
	if w.progress.converted != nil {
		t.Fatal("a failed load was recorded as converted")
	}
}

func TestDockerConvertPutsTheOldServerBackWhenTheLoadNeverEnds(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.load = ""
	if outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	w.assertPutBack(t)
}

func TestDockerConvertPutsTheOldServerBackAtEveryStep(t *testing.T) {
	for _, step := range []string{"scratch:postgres:17", "fence:postgres:16", "dump", "stop", "rename:" + provisioner.PreUpgradeContainerName("dk-1", "mu-1"), "create:postgres:17", "copy-to:/tmp", "start:postgres:17"} {
		t.Run(step, func(t *testing.T) {
			w := newDockerUpgradeWorld(t)
			w.engine.fail[step] = errors.New("engine refused")
			if outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
				t.Fatalf("outcome = %+v", outcome)
			}
			w.assertPutBack(t)
		})
	}
}

// The mark may have committed, so recovery decides from the run's row.
func TestDockerConvertLeavesAConversionItCannotRecordForRecovery(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.progress.convertErr = errors.New("platform database down")
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Unsettled || outcome.RolledBack || outcome.Converted {
		t.Fatalf("outcome = %+v", outcome)
	}
	fresh := w.engine.find(provisioner.DatabaseContainerName("dk-1"))
	if fresh == nil || fresh.id == "old-id" || w.recorded() != fresh.id {
		t.Fatalf("new container = %+v, project records %q", fresh, w.recorded())
	}
	if w.engine.find(provisioner.PreUpgradeContainerName("dk-1", "mu-1")) == nil {
		t.Fatal("the old container is gone")
	}
}

func TestDockerConvertChecksTheNewImageHasEveryExtensionFirst(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.extensions = []string{"plpgsql"}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.RolledBack || !strings.Contains(outcome.Public, `"vector"`) {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.called("fence") || w.called("stop") {
		t.Fatalf("the database was touched: %v", w.engine.calls)
	}
	w.assertPutBack(t)
}

// swappedAndRecorded is the host as a driver leaves it when it dies after
// the new container was recorded but before the conversion was.
func (w *dockerUpgradeWorld) swappedAndRecorded(image string, running bool) *engineContainer {
	old := w.engine.byID["old-id"]
	old.name, old.running, old.fenced = provisioner.PreUpgradeContainerName("dk-1", "mu-1"), false, true
	fresh := &engineContainer{id: "new-id-9", name: provisioner.DatabaseContainerName("dk-1"), image: image, running: running, fenced: true}
	w.engine.byID[fresh.id] = fresh
	row, _ := w.store.FindByProjectID("dk-1")
	row.Namespace = fresh.id
	_ = w.store.Update(row)
	w.inst.Namespace = fresh.id
	return fresh
}

// A loaded new container on the new major is the conversion done: recovery
// records it, and the run finishes forward rather than being put back.
func TestDockerRecoverRecordsALoadedConversion(t *testing.T) {
	for _, running := range []bool{true, false} {
		t.Run(fmt.Sprintf("running=%v", running), func(t *testing.T) {
			w := newDockerUpgradeWorld(t)
			fresh := w.swappedAndRecorded("postgres:17", running)
			outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress)
			if !outcome.Converted || outcome.RolledBack {
				t.Fatalf("outcome = %+v (calls %v)", outcome, w.engine.calls)
			}
			w.assertCarriedForward(t, fresh)
		})
	}
}

// the conversion is recorded, the new container serves under the project's
// name, and the old one waits stopped for cleanup.
func (w *dockerUpgradeWorld) assertCarriedForward(t *testing.T, fresh *engineContainer) {
	t.Helper()
	if w.progress.converted == nil {
		t.Fatal("the conversion was not recorded")
	}
	if !fresh.running || w.engine.find(provisioner.DatabaseContainerName("dk-1")) != fresh || w.recorded() != fresh.id {
		t.Fatalf("new container = %+v, project records %q", fresh, w.recorded())
	}
	if old := w.engine.find(provisioner.PreUpgradeContainerName("dk-1", "mu-1")); old == nil || old.running {
		t.Fatalf("old container = %+v", old)
	}
}

// The record failing again leaves the run open for the next sweep.
func TestDockerRecoverLeavesAConversionItStillCannotRecordOpen(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.swappedAndRecorded("postgres:17", true)
	w.progress.convertErr = errors.New("platform database down")
	outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress)
	if !outcome.Unsettled || outcome.RolledBack || outcome.Converted {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.engine.find("new-id-9") == nil || w.recorded() != "new-id-9" {
		t.Fatal("the converted container was put back")
	}
}

// A new container that does not serve the new major is not taken as done.
func TestDockerRecoverPutsBackANewContainerOnTheWrongMajor(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.swappedAndRecorded("postgres:16", true)
	if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.progress.converted != nil {
		t.Fatal("recorded as converted")
	}
	w.assertPutBack(t)
}

// A loaded new container the project does not record yet is not the
// conversion: the old one is put back.
func TestDockerRecoverPutsBackALoadedContainerThatWasNeverRecorded(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.swappedAndRecorded("postgres:17", true)
	row, _ := w.store.FindByProjectID("dk-1")
	row.Namespace = "old-id"
	_ = w.store.Update(row)
	w.inst.Namespace = "old-id"
	if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.progress.converted != nil {
		t.Fatal("recorded as converted")
	}
	w.assertPutBack(t)
}

// A recorded new container whose load failed is put back.
func TestDockerRecoverPutsBackARecordedContainerWhoseLoadFailed(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.load = provisioner.UpgradeFailed
	w.swappedAndRecorded("postgres:17", true)
	if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.progress.converted != nil {
		t.Fatal("recorded as converted")
	}
	w.assertPutBack(t)
}

// (a) Recovery takes the run over while the driver dumps: the driver stops
// there and renames, creates and records nothing, nor puts anything back.
func TestDockerConvertStopsWhenTheRunIsTakenOverDuringTheDump(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.after = map[string]func(){"dump": func() { w.progress.lost = true }}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !errors.Is(outcome.Cause, errRunTakenOver) || outcome.RolledBack || outcome.Converted {
		t.Fatalf("outcome = %+v", outcome)
	}
	for _, step := range []string{"stop", "rename:", "create:postgres:17", "unfence:"} {
		if w.called(step) {
			t.Fatalf("%s ran after the run was lost: %v", step, w.engine.calls)
		}
	}
	if w.recorded() != "old-id" || w.engine.find(provisioner.DatabaseContainerName("dk-1")).id != "old-id" {
		t.Fatalf("project records %q", w.recorded())
	}
}

// A run lost once the new container has loaded: the driver does not record
// it, and leaves both containers for recovery.
func TestDockerConvertDoesNotRecordAContainerForARunItLost(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.after = map[string]func(){"start:postgres:17": func() { w.progress.lost = true }}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !errors.Is(outcome.Cause, errRunTakenOver) || outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.recorded() != "old-id" {
		t.Fatalf("project records %q", w.recorded())
	}
	fresh := w.engine.find(provisioner.DatabaseContainerName("dk-1"))
	if w.engine.find(provisioner.PreUpgradeContainerName("dk-1", "mu-1")) == nil || fresh == nil || fresh.id == "old-id" {
		t.Fatalf("the swap was undone: %v", w.engine.calls)
	}
}

// A run lost once the old server stopped: the old container keeps its name.
func TestDockerConvertDoesNotRenameForARunLostAfterTheStop(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.engine.after = map[string]func(){"stop": func() { w.progress.lost = true }}
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !errors.Is(outcome.Cause, errRunTakenOver) || outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.called("rename:") || w.called("create:postgres:17") {
		t.Fatalf("the swap went on after the run was lost: %v", w.engine.calls)
	}
	if old := w.engine.find(provisioner.DatabaseContainerName("dk-1")); old == nil || old.id != "old-id" {
		t.Fatalf("old container = %+v", old)
	}
}

// The heartbeat fence cancels the driver mid-swap while the platform
// database is unreachable: the cancellation's cause says the run is lost, so
// nothing on the host is undone behind recovery's back.
func TestDockerConvertLeavesTheHostAloneOnceTheFenceCancelsIt(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	w.engine.after = map[string]func(){"create:postgres:17": func() {
		cancel(fmt.Errorf("%w: no heartbeat landed", errRunTakenOver))
		w.progress.holdErr = errors.New("dial tcp: connection refused")
		w.engine.fail["copy-to:/"] = context.Canceled
		w.engine.calls = append(w.engine.calls, "fenced")
	}}
	outcome := w.upgrader.Convert(ctx, w.inst, w.run(), w.progress)
	if outcome.RolledBack || !errors.Is(outcome.Cause, errRunTakenOver) {
		t.Fatalf("outcome = %+v", outcome)
	}
	fenced := false
	for _, call := range w.engine.calls {
		if call == "fenced" {
			fenced = true
			continue
		}
		if fenced && (strings.HasPrefix(call, "remove:") || strings.HasPrefix(call, "rename:") ||
			strings.HasPrefix(call, "start:") || strings.HasPrefix(call, "unfence:")) {
			t.Fatalf("%s ran after the fence: %v", call, w.engine.calls)
		}
	}
}

// A run already lost is not touched at all.
func TestDockerConvertChangesNothingForARunItLost(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.progress.lost = true
	outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress)
	if !errors.Is(outcome.Cause, errRunTakenOver) || outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	if w.called("fence") || w.called("scratch") {
		t.Fatalf("the database was touched: %v", w.engine.calls)
	}
}

func TestDockerRecover(t *testing.T) {
	t.Run("after the swap", func(t *testing.T) {
		w := newDockerUpgradeWorld(t)
		w.engine.load = ""
		old := w.engine.byID["old-id"]
		old.name, old.running, old.fenced = provisioner.PreUpgradeContainerName("dk-1", "mu-1"), false, true
		w.engine.byID["new-id-9"] = &engineContainer{id: "new-id-9", name: provisioner.DatabaseContainerName("dk-1"), image: "postgres:17", running: true}
		row, _ := w.store.FindByProjectID("dk-1")
		row.Namespace = "new-id-9"
		_ = w.store.Update(row)
		w.inst.Namespace = "new-id-9"
		if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
			t.Fatalf("outcome = %+v", outcome)
		}
		w.assertPutBack(t)
	})
	t.Run("before the swap", func(t *testing.T) {
		w := newDockerUpgradeWorld(t)
		w.engine.byID["old-id"].fenced = true
		if outcome := w.upgrader.Recover(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
			t.Fatalf("outcome = %+v", outcome)
		}
		w.assertPutBack(t)
	})
}

func TestDockerFinishingSteps(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	if outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress); !outcome.Converted {
		t.Fatalf("outcome = %+v", outcome)
	}
	fresh := w.engine.find(provisioner.DatabaseContainerName("dk-1"))
	fresh.fenced = true
	if err := w.upgrader.WaitServing(context.Background(), w.inst); err != nil || fresh.fenced {
		t.Fatalf("WaitServing = %v, fenced %v", err, fresh.fenced)
	}
	if warnings := w.upgrader.AfterSteps(context.Background(), w.inst); len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	w.upgrader.Cleanup(context.Background(), w.inst, w.run())
	if w.engine.find(provisioner.PreUpgradeContainerName("dk-1", "mu-1")) != nil || !w.called("cleanup:postgres:17") {
		t.Fatalf("leftovers: %v", w.engine.calls)
	}
	w.engine.fail["after"] = errors.New("vacuumdb failed")
	if warnings := w.upgrader.AfterSteps(context.Background(), w.inst); len(warnings) != 1 {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestDockerPreflight(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	blockers, err := w.upgrader.Preflight(context.Background(), w.inst, "17")
	if err != nil || len(blockers) != 0 || !w.called("preflight") {
		t.Fatalf("blockers = %v, %v", blockers, err)
	}
	w.engine.fail["preflight"] = errors.New("exec failed")
	if _, err := w.upgrader.Preflight(context.Background(), w.inst, "17"); !errors.Is(err, ErrMajorUpgradeCheckUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestDockerPreflightNeedsAnEngineThatRenames(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	adapter, store, _, _, _ := setupDockerAdapter(t)
	adapter.SetDockerClient(&fakeDockerClientForAdapter{})
	adapter.SetInstanceStore(store)
	upgrader := NewDockerMajorUpgrader(DockerMajorUpgraderConfig{Backups: adapter})
	blockers, err := upgrader.Preflight(context.Background(), w.inst, "17")
	if err != nil || len(blockers) != 1 {
		t.Fatalf("blockers = %v, %v", blockers, err)
	}
}
