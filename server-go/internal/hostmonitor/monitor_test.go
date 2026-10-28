package hostmonitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type fakeEngine struct {
	mu       sync.Mutex
	managed  []enginestats.Container
	platform map[string]enginestats.Container
	stats    map[string]enginestats.Stats
	statsErr map[string]error
	calls    []string
}

func (e *fakeEngine) Managed(context.Context) ([]enginestats.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, "list")
	return append([]enginestats.Container{}, e.managed...), nil
}

func (e *fakeEngine) Inspect(_ context.Context, ref string) (enginestats.Container, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, "inspect "+ref)
	if container, ok := e.platform[ref]; ok {
		return container, nil
	}
	for _, container := range e.managed {
		if container.ID == ref || container.Name == ref {
			return container, nil
		}
	}
	return enginestats.Container{}, enginestats.ErrNotFound
}

func (e *fakeEngine) Stats(_ context.Context, ref string) (enginestats.Stats, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, "stats "+ref)
	if err := e.statsErr[ref]; err != nil {
		return enginestats.Stats{}, err
	}
	return e.stats[ref], nil
}

type fakeStore struct {
	mu         sync.Mutex
	hosts      []hostmetrics.Sample
	containers [][]domain.ContainerSample
	averages   hostmetrics.Averages
	settings   *domain.MonitoringAlertSettings
	prunes     int
}

func (s *fakeStore) AppendHostSample(_ context.Context, sample hostmetrics.Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts = append(s.hosts, sample)
	return nil
}

func (s *fakeStore) AppendContainerSamples(_ context.Context, samples []domain.ContainerSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.containers = append(s.containers, samples)
	return nil
}

func (s *fakeStore) HostAverages(context.Context, time.Time) (hostmetrics.Averages, error) {
	return s.averages, nil
}

func (s *fakeStore) PruneHostMonitoring(context.Context, time.Time) error {
	s.prunes++
	return nil
}

func (s *fakeStore) GetMonitoringAlertSettings(context.Context) (domain.MonitoringAlertSettings, bool, error) {
	if s.settings == nil {
		return domain.MonitoringAlertSettings{}, false, nil
	}
	return *s.settings, true, nil
}

func (s *fakeStore) last() []domain.ContainerSample {
	return s.containers[len(s.containers)-1]
}

type fakeInstances []*domain.DatabaseInstance

func (f fakeInstances) FindAll() ([]*domain.DatabaseInstance, error) { return f, nil }

type recordedNotifier struct {
	mu      sync.Mutex
	changes []service.AlertChange
}

func (n *recordedNotifier) Notify(_ context.Context, _ domain.MonitoringAlertSettings, change service.AlertChange) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.changes = append(n.changes, change)
}

func hostFiles(t *testing.T) hostmetrics.Paths {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"proc/stat":      "cpu  100 0 100 700 100 0 0 0\ncpu0 1 0 0 0 0 0 0 0\ncpu1 1 0 0 0 0 0 0 0\ncpu2 1 0 0 0 0 0 0 0\ncpu3 1 0 0 0 0 0 0 0\n",
		"proc/meminfo":   "MemTotal: 8000000 kB\nMemAvailable: 4000000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n",
		"proc/loadavg":   "1.00 2.00 3.00 1/100 1\n",
		"proc/diskstats": "",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hostmetrics.Paths{Proc: filepath.Join(root, "proc"), Sys: filepath.Join(root, "sys"), Disk: root}
}

func running(id, name string, labels map[string]string) enginestats.Container {
	return enginestats.Container{ID: id, Name: name, State: "running", Labels: labels}
}

func newTestMonitor(t *testing.T, engine *fakeEngine, store *fakeStore, instances fakeInstances) (*Monitor, *recordedNotifier, *time.Time) {
	t.Helper()
	notifier := &recordedNotifier{}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	monitor := New(Deps{
		Engine: engine, Store: store, Instances: instances, Alerts: service.NewAlertingService(t.TempDir()),
		Notifier: notifier, Paths: hostFiles(t), Platform: []string{"excalibase-postgres", "excalibase-nats"},
	})
	monitor.now = func() time.Time { return now }
	return monitor, notifier, &now
}

func project() fakeInstances {
	return fakeInstances{{ProjectID: "p1", ProjectName: "shop", Status: "ACTIVE", DBType: domain.PostgreSQL,
		DeploymentMode: domain.ModeDocker, Namespace: "db-id"}}
}

func baseEngine() *fakeEngine {
	return &fakeEngine{
		managed: []enginestats.Container{
			running("db-id", "excalibase-shop-postgres", map[string]string{"excalibase.managed": "true"}),
			running("w-id", "excalibase-p1-watcher", map[string]string{"excalibase.managed": "true"}),
			running("app-id", "excalibase-app-1", map[string]string{"excalibase.managed": "true", "excalibase.component": "app", "excalibase.project": "p1"}),
			running("tool-id", "excalibase-app-disk-tool-1", map[string]string{"excalibase.managed": "true", "excalibase.component": "app-disk-tool"}),
		},
		platform: map[string]enginestats.Container{
			"excalibase-postgres": {ID: "pg-id", Name: "excalibase-postgres", State: "running", Health: "healthy"},
			"excalibase-nats":     {ID: "nats-id", Name: "excalibase-nats", State: "running"},
		},
		stats:    map[string]enginestats.Stats{},
		statsErr: map[string]error{},
	}
}

func setStats(engine *fakeEngine, at time.Time, cpuNanos uint64, rx uint64) {
	for _, ref := range []string{"db-id", "w-id", "app-id", "excalibase-postgres", "excalibase-nats"} {
		engine.stats[ref] = enginestats.Stats{Read: at, CPUNanos: cpuNanos, OnlineCPUs: 4, MemoryBytes: 100 << 20,
			MemoryLimitBytes: 512 << 20, NetOK: true, NetRx: rx, NetTx: rx / 2}
	}
}

func TestSampleWritesTheHostAndEveryContainerAttributedToItsProject(t *testing.T) {
	engine, store := baseEngine(), &fakeStore{}
	monitor, _, now := newTestMonitor(t, engine, store, project())
	setStats(engine, *now, 1e9, 6000)
	monitor.Sample(context.Background())

	if len(store.hosts) != 1 || store.hosts[0].CPUCount == nil || *store.hosts[0].CPUCount != 4 || store.hosts[0].MemoryUsedBytes == nil {
		t.Fatalf("host rows = %+v", store.hosts)
	}
	byName := map[string]domain.ContainerSample{}
	for _, sample := range store.last() {
		byName[sample.Name] = sample
	}
	if _, ok := byName["excalibase-app-disk-tool-1"]; ok {
		t.Error("a short-lived disk tool is not monitored")
	}
	want := map[string][2]string{
		"excalibase-shop-postgres": {"p1", domain.ContainerDatabase},
		"excalibase-p1-watcher":    {"p1", domain.ContainerWatcher},
		"excalibase-app-1":         {"p1", domain.ContainerApp},
		"excalibase-postgres":      {"", domain.ContainerPlatform},
		"excalibase-nats":          {"", domain.ContainerPlatform},
	}
	for name, attribution := range want {
		sample, ok := byName[name]
		if !ok || sample.ProjectID != attribution[0] || sample.Component != attribution[1] || !sample.Timestamp.Equal(*now) {
			t.Errorf("%s = %+v, want project %q component %q", name, sample, attribution[0], attribution[1])
		}
		// A first sample has memory but no rates yet.
		if sample.MemoryBytes == nil || *sample.MemoryBytes != 100<<20 || sample.CPUCores != nil || sample.NetworkRxBytesPerSec != nil {
			t.Errorf("%s first sample = %+v", name, sample)
		}
	}
	if byName["excalibase-postgres"].Health != "healthy" {
		t.Errorf("platform health = %q", byName["excalibase-postgres"].Health)
	}
}

func TestTheSecondSampleHasCPUAndNetworkRates(t *testing.T) {
	engine, store := baseEngine(), &fakeStore{}
	monitor, _, now := newTestMonitor(t, engine, store, project())
	setStats(engine, *now, 1e9, 6000)
	monitor.Sample(context.Background())
	*now = now.Add(time.Minute)
	setStats(engine, *now, 1e9+30e9, 6000+60*1000)
	monitor.Sample(context.Background())

	for _, sample := range store.last() {
		if sample.CPUCores == nil || *sample.CPUCores != 0.5 {
			t.Errorf("%s cpu = %v, want 30s of CPU in 60s = 0.5 cores", sample.Name, sample.CPUCores)
			continue
		}
		if *sample.CPUPercent != 12.5 || *sample.NetworkRxBytesPerSec != 1000 || *sample.NetworkTxBytesPerSec != 500 || *sample.MemoryLimitBytes != 512<<20 {
			t.Errorf("%s = %+v", sample.Name, sample)
		}
	}
	host := store.hosts[1]
	// No host network file: the host's figure is the containers' sum, and says so.
	if host.NetworkSource == nil || *host.NetworkSource != hostmetrics.NetworkFromContainers || *host.NetworkRxBytesPerSec != 5*1000 {
		t.Errorf("host network = %v %v", host.NetworkSource, host.NetworkRxBytesPerSec)
	}
	latest, ok := monitor.Latest("db-id")
	if !ok || latest.CPUCores == nil || *latest.CPUCores != 0.5 {
		t.Errorf("Latest(db-id) = %+v %v", latest, ok)
	}
}

func TestAContainerWithoutALimitReportsNone(t *testing.T) {
	engine, store := baseEngine(), &fakeStore{}
	monitor, _, now := newTestMonitor(t, engine, store, project())
	setStats(engine, *now, 1, 1)
	unlimited := engine.stats["excalibase-nats"]
	unlimited.MemoryLimitBytes = 8000000 * 1024
	engine.stats["excalibase-nats"] = unlimited
	monitor.Sample(context.Background())
	for _, sample := range store.last() {
		if sample.Name == "excalibase-nats" && sample.MemoryLimitBytes != nil {
			t.Errorf("a limit as large as the host is no limit: %v", *sample.MemoryLimitBytes)
		}
	}
}

func TestStoppedAndMissingContainersHaveAStateAndNoFigures(t *testing.T) {
	engine, store := baseEngine(), &fakeStore{}
	engine.managed[0].State = "exited"
	delete(engine.platform, "excalibase-nats")
	monitor, _, now := newTestMonitor(t, engine, store, project())
	setStats(engine, *now, 1, 1)
	engine.statsErr["w-id"] = enginestats.ErrNotRunning
	monitor.Sample(context.Background())
	byName := map[string]domain.ContainerSample{}
	for _, sample := range store.last() {
		byName[sample.Name] = sample
	}
	if db := byName["excalibase-shop-postgres"]; db.State != "exited" || db.MemoryBytes != nil {
		t.Errorf("stopped database = %+v", db)
	}
	if nats := byName["excalibase-nats"]; nats.State != StateMissing || nats.Component != domain.ContainerPlatform {
		t.Errorf("missing platform container = %+v", nats)
	}
	if watcher := byName["excalibase-p1-watcher"]; watcher.State != "exited" || watcher.CPUCores != nil {
		t.Errorf("a container that stopped between list and stats = %+v", watcher)
	}
	for _, call := range engine.calls {
		if call == "stats db-id" {
			t.Error("a stopped container is not asked for stats")
		}
	}
}

func TestAHungStatsCallDoesNotHoldTheSample(t *testing.T) {
	engine, store := baseEngine(), &fakeStore{}
	monitor, _, now := newTestMonitor(t, engine, store, project())
	setStats(engine, *now, 1, 1)
	engine.statsErr["app-id"] = errors.New("engine timeout")
	monitor.Sample(context.Background())
	if len(store.last()) != 5 {
		t.Errorf("one failed container still leaves the others' rows: %d", len(store.last()))
	}
}

func TestPruneRunsHourly(t *testing.T) {
	engine, store := baseEngine(), &fakeStore{}
	monitor, _, now := newTestMonitor(t, engine, store, project())
	monitor.Sample(context.Background())
	*now = now.Add(30 * time.Minute)
	monitor.Sample(context.Background())
	*now = now.Add(31 * time.Minute)
	monitor.Sample(context.Background())
	if store.prunes != 2 {
		t.Errorf("prunes = %d, want at start and after an hour", store.prunes)
	}
}

// A project's display name is free text: it never claims a container, and a
// name two projects would claim is credited to neither.
func TestAttributionNeverFollowsADisplayName(t *testing.T) {
	victim := &domain.DatabaseInstance{ProjectID: "victim", DeploymentMode: domain.ModeDocker, Namespace: "org-ref"}
	attacker := &domain.DatabaseInstance{ProjectID: "attacker", ProjectName: "victim", DeploymentMode: domain.ModeDocker}
	for _, order := range [][]*domain.DatabaseInstance{{victim, attacker}, {attacker, victim}} {
		credit := attribution(order)
		if project, _ := credit(enginestats.Container{ID: "c1", Name: provisioner.DatabaseContainerName("victim")}); project != "victim" {
			t.Errorf("the victim's database is credited to %q", project)
		}
	}
	twin := &domain.DatabaseInstance{ProjectID: "victim", DeploymentMode: domain.ModeDocker}
	other := &domain.DatabaseInstance{ProjectID: "victim", DeploymentMode: domain.ModeDocker, OrgID: "other"}
	if project, _ := attribution([]*domain.DatabaseInstance{twin, other})(enginestats.Container{Name: provisioner.WatcherContainerName("victim")}); project != "victim" {
		t.Errorf("one project's two rows still name it: %q", project)
	}
}
