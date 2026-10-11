package provisioner

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// fakeHostPorts hands each project one port for good, as the allocator does.
type fakeHostPorts struct {
	held     map[string]int
	released []string
	next     int
	err      error
}

func (f *fakeHostPorts) Allocate(_ context.Context, projectID string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	if port, ok := f.held[projectID]; ok {
		return port, nil
	}
	f.next++
	f.held[projectID] = 30000 + f.next
	return f.held[projectID], nil
}

// AllocateMongo holds a DocumentDB project's second port under its own key.
func (f *fakeHostPorts) AllocateMongo(ctx context.Context, projectID string) (int, error) {
	return f.Allocate(ctx, projectID+mongoPortKey)
}

const mongoPortKey = "#mongo"

func (f *fakeHostPorts) Release(_ context.Context, projectID string) error {
	delete(f.held, projectID)
	delete(f.held, projectID+mongoPortKey)
	f.released = append(f.released, projectID)
	return nil
}

// EXC-592: a project's clients keep one host port: through a restart, an
// in-place restore and an upgrade the container is published on the port the
// project holds, and the port goes back only when the project is deleted.
func TestDockerMySQLPublishesOnTheProjectsOwnHostPort(t *testing.T) {
	engine := newFakeEngine()
	ports := &fakeHostPorts{held: map[string]int{}}
	mysql := newTestDockerMySQL(engine)
	mysql.SetHostPorts(ports)
	result, err := mysql.Provision(context.Background(), dockerMySQLRequest(), testTier, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := engine.specs[result.Namespace].Ports["3306"]; got != "30001" {
		t.Fatalf("published on %q", got)
	}
	if err := mysql.RemoveContainer(context.Background(), result.Namespace); err != nil || len(ports.released) != 0 {
		t.Fatalf("a rebuild gave the port back: %v %v", err, ports.released)
	}
	image, _ := config.MySQLImage(config.DefaultMySQLVersion())
	if _, err := mysql.StartOnVolume(context.Background(), "p1", image, testTier); err != nil {
		t.Fatal(err)
	}
	if got := engine.specs[result.Namespace].Ports["3306"]; got != "30001" {
		t.Fatalf("rebuilt on %q", got)
	}
	if err := mysql.Deprovision(context.Background(), result.Namespace, "p1"); err != nil || len(ports.released) != 1 {
		t.Fatalf("deleted project kept its port: %v %v", err, ports.released)
	}
}

func TestDockerMySQLWithoutAPortCreatesNothing(t *testing.T) {
	engine := newFakeEngine()
	mysql := newTestDockerMySQL(engine)
	mysql.SetHostPorts(&fakeHostPorts{held: map[string]int{}, err: errors.New("ports exhausted")})
	if _, err := mysql.Provision(context.Background(), dockerMySQLRequest(), testTier, func(domain.ProvisioningStage) {}); err == nil {
		t.Fatal("provisioned without a port")
	}
	if len(engine.specs) != 0 {
		t.Fatalf("created %v", engine.specs)
	}
}

// A rebuilt container takes the port its predecessor just freed; rootless
// Podman can hold it a moment longer, so the start is retried, not failed.
func TestDockerMySQLStartWaitsForItsJustFreedPort(t *testing.T) {
	engine := newFakeEngine()
	engine.portBusy = 2
	mysql := newTestDockerMySQL(engine)
	mysql.SetHostPorts(&fakeHostPorts{held: map[string]int{}})
	image, _ := config.MySQLImage(config.DefaultMySQLVersion())
	if _, err := mysql.StartOnVolume(context.Background(), "p1", image, testTier); err != nil {
		t.Fatalf("start: %v", err)
	}
	if !engine.containers["excalibase-p1-mysql"].running {
		t.Fatal("not running")
	}
	engine.failOn = "start:excalibase-p2-mysql"
	if _, err := mysql.StartOnVolume(context.Background(), "p2", image, testTier); err == nil {
		t.Fatal("any other start failure is retried")
	}
}

func TestDockerMySQLResumeWaitsForItsHeldPort(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDockerMySQL(t, engine)
	mysql := newTestDockerMySQL(engine)
	if err := mysql.Pause(context.Background(), result.Namespace, "p1"); err != nil {
		t.Fatal(err)
	}
	engine.portBusy = 2
	if err := mysql.Resume(context.Background(), result.Namespace, "p1"); err != nil {
		t.Fatalf("resume: %v", err)
	}
}
