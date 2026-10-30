package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
)

// newSingleHostDrainFixture is a Docker/Podman install: its projects' lines
// are in the bundled Loki under the shipper's labels (EXC-658 on EXC-672).
func newSingleHostDrainFixture(t *testing.T) *drainFixture {
	t.Helper()
	f := newDrainFixture(t)
	f.instances["proj-e"] = &domain.DatabaseInstance{ProjectID: "proj-e", DBType: domain.MySQL, DeploymentMode: domain.ModeDocker}
	f.instances["Bad_ID"] = &domain.DatabaseInstance{ProjectID: "Bad_ID", DBType: domain.PostgreSQL, DeploymentMode: domain.ModeDocker}
	f.svc = NewLogDrainService(LogDrainDeps{Store: f.store, Vault: f.vault, Instances: f.svc.instances, Logs: f.reader,
		Now: f.svc.now, SingleHost: true})
	return f
}

func TestLogDrains_SingleHostProjectIsAvailable(t *testing.T) {
	f := newSingleHostDrainFixture(t)
	if _, err := f.svc.Create(context.Background(), "proj-d", "u", CreateLogDrainInput{Name: "x", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "k"}); err != nil {
		t.Fatalf("docker project on a single host: %v", err)
	}
	noLoki := NewLogDrainService(LogDrainDeps{Store: f.store, Vault: f.vault, Instances: f.svc.instances, Now: f.svc.now, SingleHost: true})
	if _, err := noLoki.Create(context.Background(), "proj-d", "u", CreateLogDrainInput{Name: "y", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "k"}); !errors.Is(err, ErrLogDrainsUnavailable) {
		t.Errorf("single host without its Loki: %v", err)
	}
	if _, err := f.svc.Create(context.Background(), "Bad_ID", "u", CreateLogDrainInput{Name: "z", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "k"}); !errors.Is(err, ErrLogDrainsUnavailable) {
		t.Errorf("a project id no selector takes: %v", err)
	}
}

func TestLogDrains_SingleHostPullReadsTheShippersLabelsPerProject(t *testing.T) {
	f := newSingleHostDrainFixture(t)
	drainD, drainE := f.datadog(t, "proj-d"), f.datadog(t, "proj-e")
	ts := drainNow.UnixNano() + 1
	f.reader.lines[`{project_id="proj-d",component="database"}`] = []logdrain.Line{{TS: ts, Line: "line of d"}}
	f.reader.lines[`{project_id="proj-e",component="database"}`] = []logdrain.Line{{TS: ts, Line: "line of e"}}
	f.now = drainNow.Add(time.Minute)
	batches, err := f.svc.Pull(context.Background(), 8)
	if err != nil || len(batches) != 2 {
		t.Fatalf("Pull = %d batches, %v", len(batches), err)
	}
	want := map[string]string{drainD.ID: "line of d", drainE.ID: "line of e"}
	for _, batch := range batches {
		if len(batch.Lines) != 1 || batch.Lines[0].Line != want[batch.DrainID] {
			t.Errorf("drain %s (%s) got %+v", batch.DrainID, batch.ProjectID, batch.Lines)
		}
	}
	for _, query := range f.reader.queries {
		if query != `{project_id="proj-d",component="database"}` && query != `{project_id="proj-e",component="database"}` {
			t.Errorf("read with %s", query)
		}
	}
}

// A single host's Loki holds only its own containers' lines: a Kubernetes
// project there would drain nothing, so it is refused rather than silent.
func TestLogDrains_SingleHostRefusesAKubernetesProject(t *testing.T) {
	f := newSingleHostDrainFixture(t)
	if _, err := f.svc.Create(context.Background(), "proj-a", "u", CreateLogDrainInput{Name: "x", Kind: logdrain.KindDatadog, DatadogSite: "us1", Secret: "k"}); !errors.Is(err, ErrLogDrainsUnavailable) {
		t.Errorf("kubernetes project on a single host: %v", err)
	}
}

func TestLogDrains_SingleHostProjectWhoseIDChangedShapeFails(t *testing.T) {
	f := newSingleHostDrainFixture(t)
	drain := f.datadog(t, "proj-d")
	f.instances["proj-d"] = &domain.DatabaseInstance{ProjectID: "proj-d\"}", DeploymentMode: domain.ModeDocker}
	f.now = drainNow.Add(time.Minute)
	batches, err := f.svc.Pull(context.Background(), 8)
	if err != nil || len(batches) != 0 || len(f.reader.queries) != 0 {
		t.Fatalf("Pull = %v, %v, queries %v", batches, err, f.reader.queries)
	}
	if got := f.store.Items[drain.ID]; got.LastError == "" {
		t.Errorf("drain not failed: %+v", got)
	}
}

// The single-host shipper finds a new container on its 30 s scan and then
// pushes lines stamped when they were written, so a drain there reads no
// closer to now than singleHostIngestDelay or it passes them by.
func TestLogDrains_SingleHostReadsBehindTheShippersScan(t *testing.T) {
	f := newSingleHostDrainFixture(t)
	f.datadog(t, "proj-d")
	f.now = drainNow.Add(2 * time.Minute)
	if _, err := f.svc.Pull(context.Background(), 8); err != nil {
		t.Fatal(err)
	}
	if len(f.reader.readArgs) != 1 || f.reader.readArgs[0][1] != f.now.Add(-singleHostIngestDelay).UnixNano() {
		t.Fatalf("read window %v", f.reader.readArgs)
	}
	if singleHostIngestDelay <= 30*time.Second {
		t.Errorf("single-host ingest delay %v does not cover the shipper's 30 s scan", singleHostIngestDelay)
	}
}
