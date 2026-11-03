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
}

type engineContainer struct {
	id, name, image string
	running, fenced bool
	env             map[string]string
	cmd             []string
}

func newUpgradeEngine() *upgradeEngine {
	engine := &upgradeEngine{byID: map[string]*engineContainer{}, load: provisioner.UpgradeLoaded,
		fail: map[string]error{}, extensions: []string{"plpgsql", "vector", "citext"}, copied: map[string][]string{}}
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
	e.calls = append(e.calls, name)
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
	e.calls = append(e.calls, "remove:"+id)
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
	e.calls = append(e.calls, "exec:"+c.image+":"+joined)
	if strings.Contains(joined, "pg_isready") && !e.loaded(c) && e.load == "" {
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
		e.calls = append(e.calls, "unfence:"+c.image)
		c.fenced = false
	case stdin == provisioner.DumpScript():
		return "", e.step("dump")
	case stdin == provisioner.UpgradeStateScript():
		return e.load + "\n", nil
	case stdin == provisioner.CleanupScript():
		e.calls = append(e.calls, "cleanup:"+c.image)
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

func TestDockerConvertPutsTheOldServerBackWhenTheConversionCannotBeRecorded(t *testing.T) {
	w := newDockerUpgradeWorld(t)
	w.progress.convertErr = errors.New("platform database down")
	if outcome := w.upgrader.Convert(context.Background(), w.inst, w.run(), w.progress); !outcome.RolledBack {
		t.Fatalf("outcome = %+v", outcome)
	}
	w.assertPutBack(t)
	if w.inst.Namespace != "old-id" {
		t.Fatalf("inst = %q", w.inst.Namespace)
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
