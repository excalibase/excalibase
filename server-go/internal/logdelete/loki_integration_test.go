//go:build integration

package logdelete

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// lokiImage is the chart's and compose's Loki.
const lokiImage = "docker.io/grafana/loki:3.7.8@sha256:1107dd5274e0ada47e42472b7a7e71f3b2a2fe878878108f3e2f9e51528f0193"

func startLoki(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	_, here, _, _ := runtime.Caller(0)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        lokiImage,
			ExposedPorts: []string{"3100/tcp"},
			Cmd:          []string{"-config.file=/etc/loki/loki.yaml", "-target=all"},
			// The chart mounts its volume at /var/loki; Loki runs as 10001.
			Tmpfs: map[string]string{"/var/loki": "rw,uid=10001,gid=10001"},
			Files: []testcontainers.ContainerFile{{HostFilePath: filepath.Join(filepath.Dir(here), "testdata", "loki.yaml"),
				ContainerFilePath: "/etc/loki/loki.yaml", FileMode: 0o644}},
			WaitingFor: wait.ForHTTP("/ready").WithPort("3100/tcp").WithStartupTimeout(120 * time.Second),
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

func push(t *testing.T, lokiURL string, labels map[string]string, lines ...string) {
	t.Helper()
	now := time.Now()
	values := make([][2]string, 0, len(lines))
	for i, line := range lines {
		values = append(values, [2]string{strconv.FormatInt(now.Add(time.Duration(i-len(lines))*time.Second).UnixNano(), 10), line})
	}
	body, _ := json.Marshal(map[string]any{"streams": []any{map[string]any{"stream": labels, "values": values}}})
	resp, err := http.Post(lokiURL+"/loki/api/v1/push", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("push answered %d", resp.StatusCode)
	}
}

// flush moves the ingester's lines to storage, where they are by the time a
// production delete request outlives its cancel period.
func flush(t *testing.T, lokiURL string) {
	t.Helper()
	resp, err := http.Post(lokiURL+"/flush", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("flush answered %d", resp.StatusCode)
	}
}

// namespaces lists the label's values without a log query: a query would
// cache the tenant's delete requests in the querier for five minutes.
func labelValues(t *testing.T, lokiURL, label string) []string {
	t.Helper()
	resp, err := http.Get(lokiURL + "/loki/api/v1/label/" + label + "/values?start=" + strconv.FormatInt(time.Now().Add(-time.Hour).UnixNano(), 10))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data []string `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.Data
}

func countLines(t *testing.T, lokiURL, selector string) int {
	t.Helper()
	params := url.Values{"query": {selector}, "start": {strconv.FormatInt(time.Now().Add(-time.Hour).UnixNano(), 10)}, "limit": {"1000"}}
	resp, err := http.Get(lokiURL + "/loki/api/v1/query_range?" + params.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct {
		Data struct {
			Result []struct {
				Values [][2]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, stream := range body.Data.Result {
		total += len(stream.Values)
	}
	return total
}

// workUntilDone runs the worker until Loki processed the row's request. The
// test configs' cancel period is 20s and compaction 15s; checked as soon as allowed.
func workUntilDone(t *testing.T, store *memStore, worker *Worker, projectID string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for store.row(projectID).State != StateDone && time.Now().Before(deadline) {
		row := store.row(projectID)
		row.NextAttemptAt = time.Now()
		_ = store.SaveProjectLogDeletion(context.Background(), row)
		worker.RunOnce(context.Background())
		time.Sleep(5 * time.Second)
	}
	if row := store.row(projectID); row.State != StateDone {
		t.Fatalf("Loki never processed the deletion: %+v", row)
	}
}

// selectorOf is an exact selector of every label.
func selectorOf(labels map[string]string) string {
	pairs := make([]string, 0, len(labels))
	for key, value := range labels {
		pairs = append(pairs, key+"="+strconv.Quote(value))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

// EXC-678: deleting one project's lines in a real Loki removes them and only
// them: the other project's namespace, a namespace that merely ends alike, the
// platform's own lines and the single-host label all stay.
func TestRealLokiDeletesOnlyTheProjectsLines(t *testing.T) {
	lokiURL := startLoki(t)
	streams := map[string]map[string]string{
		"deleted":   {"namespace": "org1-proj-a", "app": "postgres"},
		"deleted 2": {"namespace": "org1-proj-a", "app": "excalibase-watcher"},
		"other":     {"namespace": "org1-proj-b", "app": "postgres"},
		"alike":     {"namespace": "org1-proj-a2", "app": "postgres"},
		"platform":  {"namespace": "excalibase", "app": "provisioning"},
		"host":      {"project_id": "proj-a", "component": "database"},
	}
	for name, labels := range streams {
		push(t, lokiURL, labels, name+" one", name+" two")
	}
	flush(t, lokiURL)
	inst := &domain.DatabaseInstance{ProjectID: "proj-a", Namespace: "org1-proj-a", DeploymentMode: domain.ModeK8s}
	selector, err := ProjectSelector(inst)
	if err != nil {
		t.Fatal(err)
	}
	if got := labelValues(t, lokiURL, "namespace"); !slices.Contains(got, "org1-proj-a") {
		t.Fatalf("before: namespaces %v", got)
	}

	store := newMemStore(Deletion{ProjectID: "proj-a", Selector: selector, State: StatePending,
		Start: StartFor(time.Now().Add(-time.Hour), time.Now(), 168*time.Hour), NextAttemptAt: time.Now()})
	audit := &memAudit{}
	worker := NewWorker(store, NewLokiClient(lokiURL, http.DefaultClient), audit)
	worker.RunOnce(context.Background())
	if row := store.row("proj-a"); row.State != StateRequested {
		t.Fatalf("not requested: %+v", row)
	}

	workUntilDone(t, store, worker, "proj-a")
	if got := countLines(t, lokiURL, selector); got != 0 {
		t.Errorf("after: %d lines of the deleted project remain", got)
	}
	for name, labels := range streams {
		if labels["namespace"] == "org1-proj-a" {
			continue
		}
		if got := countLines(t, lokiURL, selectorOf(labels)); got != 2 {
			t.Errorf("%s: %d of 2 lines remain", name, got)
		}
	}
	if got := audit.actions(); len(got) != 2 || got[1] != ActionDeleted+":proj-a" {
		t.Errorf("audit = %v", got)
	}
}

// Loki itself refuses a wildcard; the client refuses it before Loki is asked.
func TestRealLokiNeverSeesAWildcard(t *testing.T) {
	lokiURL := startLoki(t)
	push(t, lokiURL, map[string]string{"namespace": "org1-proj-b"}, "keep")
	client := NewLokiClient(lokiURL, http.DefaultClient)
	for _, bad := range []string{`{namespace=~".+"}`, `{namespace=~"org1-.*"}`, `{}`} {
		if err := client.Request(context.Background(), bad, time.Now().Add(-time.Hour), time.Now()); err == nil {
			t.Errorf("%s was sent", bad)
		}
	}
	resp, err := http.Get(lokiURL + "/loki/api/v1/delete")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var requests []deleteRequest
	if err := json.NewDecoder(resp.Body).Decode(&requests); err != nil || len(requests) != 0 {
		t.Fatalf("Loki holds %+v (%v)", requests, err)
	}
}
