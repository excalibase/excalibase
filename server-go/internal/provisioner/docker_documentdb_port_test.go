package provisioner

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func portedDocumentDB(engine *fakeEngine) (*DockerPostgreSQLProvisioner, *fakeHostPorts) {
	ports := &fakeHostPorts{held: map[string]int{}}
	p := NewDockerPostgreSQLProvisioner(engine)
	p.SetHostPorts(ports)
	p.SetStartPoller(NewPoller(time.Millisecond, time.Second))
	return p, ports
}

// EXC-594: a single host's DocumentDB project holds a port for Postgres and
// one for Mongo for its life; deleting it gives both back.
func TestDocumentDBPublishesThePortsItsProjectHolds(t *testing.T) {
	engine := newFakeEngine()
	p, ports := portedDocumentDB(engine)
	engine.portBusy = 2
	result, err := p.Provision(context.Background(), dockerDocumentDBRequest(), testTier, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision: %v (calls %v)", err, engine.calls)
	}
	published := engine.specs["excalibase-p1-postgres"].Ports
	if published["5432"] != "30001" || published["10260"] != "30002" {
		t.Fatalf("published %v, held %v", published, ports.held)
	}
	got, err := p.DocumentDBPorts(context.Background(), "p1")
	if err != nil || got != (DocumentDBPorts{Postgres: "30001", Mongo: "30002"}) {
		t.Fatalf("DocumentDBPorts %+v %v", got, err)
	}
	if err := p.Deprovision(context.Background(), result.Namespace, "p1"); err != nil {
		t.Fatal(err)
	}
	if len(ports.held) != 0 || len(ports.released) != 1 {
		t.Fatalf("held %v released %v", ports.held, ports.released)
	}
}

// A rebuilt gateway presents the certificate its predecessor did, so a
// client that saved the project's CA keeps verifying it.
func TestARebuiltGatewayKeepsItsCertificate(t *testing.T) {
	engine := newFakeEngine()
	provisionDocumentDB(t, engine)
	ctx := context.Background()
	kept, found, err := ReadDocumentDBGatewayTLS(ctx, engine, "p1")
	if err != nil || !found {
		t.Fatalf("read %v %v", found, err)
	}
	before, _ := DocumentDBGatewayCA(ctx, engine, "p1")
	delete(engine.copied, "excalibase-p1-documentdb:"+documentDBGatewayHome)
	if err := StartDocumentDBGatewayWithTLS(ctx, engine, "p1", "excalibase-p1-postgres", ContainerLimits{}, kept); err != nil {
		t.Fatal(err)
	}
	after, err := DocumentDBGatewayCA(ctx, engine, "p1")
	if err != nil || len(before) == 0 || !bytes.Equal(before, after) {
		t.Fatalf("CA changed: %v", err)
	}
	delete(engine.containers, "excalibase-p1-documentdb")
	if _, found, err := ReadDocumentDBGatewayTLS(ctx, engine, "p1"); found || err != nil {
		t.Fatalf("no gateway: found %v %v", found, err)
	}
}
