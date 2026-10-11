package provisioner

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func portedPostgres(docker *mockDockerClient) (*DockerPostgreSQLProvisioner, *fakeHostPorts) {
	ports := &fakeHostPorts{held: map[string]int{}}
	p := NewDockerPostgreSQLProvisioner(docker)
	p.SetHostPorts(ports)
	p.SetStartPoller(NewPoller(time.Millisecond, time.Second))
	return p, ports
}

// EXC-593: a single host's Postgres project keeps one host port for its
// life, so a restart, a pause or an in-place restore leaves its clients'
// connection strings working; deleting it releases the port.
func TestDockerPostgresPublishesThePortItsProjectHolds(t *testing.T) {
	docker := newMockDocker()
	p, ports := portedPostgres(docker)
	req := domain.ProvisioningRequest{ProjectName: "shop", DBType: domain.PostgreSQL, PostgresVersion: "17"}
	result, err := p.Provision(context.Background(), req, testTier, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if docker.lastPorts["5432"] != "30001" || ports.held["shop"] != 30001 {
		t.Fatalf("published %v, held %v", docker.lastPorts, ports.held)
	}
	if got, err := p.HostPort(context.Background(), "shop"); err != nil || got != "30001" {
		t.Fatalf("HostPort %q %v", got, err)
	}
	if err := p.Deprovision(context.Background(), result.Namespace, "shop"); err != nil {
		t.Fatal(err)
	}
	if len(ports.released) != 1 || ports.released[0] != "shop" {
		t.Fatalf("released %v", ports.released)
	}
}

func TestDockerPostgresResumeWaitsForItsHeldPort(t *testing.T) {
	docker := newMockDocker()
	p, _ := portedPostgres(docker)
	docker.containers["c1"] = "stopped"
	docker.portBusy = 2
	if err := p.Resume(context.Background(), "c1", "shop"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
}
