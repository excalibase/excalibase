//go:build integration

package hostlogs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/enginestats"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// lokiImage is the chart's and compose's Loki.
const lokiImage = "docker.io/grafana/loki:3.7.8@sha256:1107dd5274e0ada47e42472b7a7e71f3b2a2fe878878108f3e2f9e51528f0193"

// startBundledLoki runs Loki with the single-host compose file's own config.
func startBundledLoki(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	_, here, _, _ := runtime.Caller(0)
	config := filepath.Join(filepath.Dir(here), "..", "..", "..", "deploy", "single-host", "loki", "loki.yaml")
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        lokiImage,
			ExposedPorts: []string{"3100/tcp"},
			Cmd:          []string{"-config.file=/etc/loki/loki.yaml", "-config.expand-env=true"},
			Env:          map[string]string{"LOG_RETENTION": "168h", "LOG_RETENTION_FREE": "24h"},
			Files:        []testcontainers.ContainerFile{{HostFilePath: config, ContainerFilePath: "/etc/loki/loki.yaml", FileMode: 0o644}},
			WaitingFor:   wait.ForHTTP("/ready").WithPort("3100/tcp").WithStartupTimeout(120 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start loki: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "3100/tcp")
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

type downEngine struct{}

func (downEngine) Logs(context.Context, string, int, time.Time) (string, error) {
	return "", errors.New("engine log gone with the container")
}

// EXC-672: lines shipped for two projects into the bundled Loki are searched
// through the project log search; each project finds only its own.
func TestBundledLokiKeepsEachProjectsLinesApart(t *testing.T) {
	lokiURL := startBundledLoki(t)
	at := time.Now().Add(-time.Minute)
	engine := &fakeEngine{logs: map[string][]enginestats.LogLine{
		"db-a": {{At: at, Text: `ERROR:  relation "marker_a" does not exist`}},
		"db-b": {{At: at, Text: `ERROR:  relation "marker_b" does not exist {project_id="proj-a"}`}},
	}}
	shipper := New(Deps{Engine: engine, Cursors: &fakeCursors{}, Pusher: NewLokiPusher(lokiURL, http.DefaultClient),
		Flags: features.NewStatic(features.LogRetention), DailyBytes: 1 << 20})
	shipper.deps.Retention.Default = 24 * time.Hour
	for _, project := range []string{"proj-a", "proj-b"} {
		id := "db-" + project[len(project)-1:]
		target := Target{ID: id, Retention: 24 * time.Hour, Labels: Labels{ProjectID: project, Component: domain.ContainerDatabase,
			Container: provisioner.DatabaseContainerName(project), Tier: "free"}}
		if _, err := shipper.shipStream(context.Background(), target, 0); err != nil {
			t.Fatalf("ship %s: %v", project, err)
		}
	}

	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{"proj-a", "proj-b"} {
		if err := store.Create(&domain.DatabaseInstance{ProjectID: project, OrgID: "org1", Namespace: "c-" + project,
			DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker, Status: "ACTIVE", Tier: domain.Free}); err != nil {
			t.Fatal(err)
		}
	}
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), nil)
	svc.SetHostLogStore(lokiURL)
	svc.SetSingleHostLogs(downEngine{})

	search := func(project, term string) string {
		var lines string
		for i := 0; i < 30; i++ {
			page, err := svc.SearchProjectLogs(context.Background(), project, service.LogSearch{Lines: 100, Term: term, Since: time.Hour})
			if err == nil && page.Lines != "" {
				return page.Lines
			}
			time.Sleep(time.Second)
		}
		return lines
	}
	if got := search("proj-a", "marker"); !strings.Contains(got, "marker_a") || strings.Contains(got, "marker_b") {
		t.Errorf("proj-a found %q", got)
	}
	if got := search("proj-b", "marker"); !strings.Contains(got, "marker_b") || strings.Contains(got, "marker_a") {
		t.Errorf("proj-b found %q", got)
	}
	// A term shaped like a selector stays a literal filter on the project's own lines.
	page, err := svc.SearchProjectLogs(context.Background(), "proj-a", service.LogSearch{Lines: 100, Term: `"} or {project_id="proj-b`, Since: time.Hour})
	if err != nil || strings.Contains(page.Lines, "marker_b") {
		t.Errorf("injected term found %q (%v)", page.Lines, err)
	}
}
