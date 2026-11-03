//go:build integration

package logdelete

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// startSingleHostLoki runs the compose file's own Loki config with only the
// compactor's timers shortened; the test fails if those lines are not there.
func startSingleHostLoki(t *testing.T) string {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(here), "..", "..", "..", "deploy", "single-host", "loki", "loki.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	config := string(raw)
	for old, short := range map[string]string{
		"delete_request_cancel_period: 24h": "delete_request_cancel_period: 20s",
		"compaction_interval: 10m":          "compaction_interval: 15s",
	} {
		if !strings.Contains(config, old) {
			t.Fatalf("the single-host loki.yaml no longer says %q", old)
		}
		config = strings.Replace(config, old, short, 1)
	}
	path := filepath.Join(t.TempDir(), "loki.yaml")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        lokiImage,
			ExposedPorts: []string{"3100/tcp"},
			Cmd:          []string{"-config.file=/etc/loki/loki.yaml", "-config.expand-env=true", "-target=all"},
			Files:        []testcontainers.ContainerFile{{HostFilePath: path, ContainerFilePath: "/etc/loki/loki.yaml", FileMode: 0o644}},
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

// EXC-678: on a single host a deleted project's lines leave the bundled Loki
// by its project_id label, and no other project's do.
func TestSingleHostLokiDeletesOnlyTheProjectsLines(t *testing.T) {
	lokiURL := startSingleHostLoki(t)
	streams := map[string]map[string]string{
		"deleted": {"project_id": "proj-a", "component": "database", "container": "excalibase-proj-a-postgres"},
		"other":   {"project_id": "proj-b", "component": "database", "container": "excalibase-proj-b-postgres"},
		"alike":   {"project_id": "proj-a2", "component": "database", "container": "excalibase-proj-a2-postgres"},
	}
	for name, labels := range streams {
		push(t, lokiURL, labels, name+" one", name+" two")
	}
	flush(t, lokiURL)
	if got := labelValues(t, lokiURL, "project_id"); !slices.Contains(got, "proj-a") {
		t.Fatalf("before: project ids %v", got)
	}
	selector, err := ProjectSelector(&domain.DatabaseInstance{ProjectID: "proj-a", Namespace: "excalibase-proj-a-postgres", DeploymentMode: domain.ModeDocker})
	if err != nil || selector != `{project_id="proj-a"}` {
		t.Fatalf("selector = %q %v", selector, err)
	}
	store := newMemStore(Deletion{ProjectID: "proj-a", Selector: selector, State: StatePending,
		Start: StartFor(time.Now().Add(-time.Hour), time.Now(), 168*time.Hour), NextAttemptAt: time.Now()})
	worker := NewWorker(store, NewLokiClient(lokiURL, http.DefaultClient), &memAudit{})
	deadline := time.Now().Add(4 * time.Minute)
	for store.row("proj-a").State != StateDone && time.Now().Before(deadline) {
		row := store.row("proj-a")
		row.NextAttemptAt = time.Now()
		_ = store.SaveProjectLogDeletion(context.Background(), row)
		worker.RunOnce(context.Background())
		time.Sleep(5 * time.Second)
	}
	if row := store.row("proj-a"); row.State != StateDone {
		t.Fatalf("Loki never processed the deletion: %+v", row)
	}
	if got := countLines(t, lokiURL, selector); got != 0 {
		t.Errorf("%d lines of the deleted project remain", got)
	}
	for _, kept := range []string{"proj-b", "proj-a2"} {
		if got := countLines(t, lokiURL, `{project_id="`+kept+`"}`); got != 2 {
			t.Errorf("%s: %d of 2 lines remain", kept, got)
		}
	}
}
