package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A single host publishes a project's ports on its own loopback (EXC-576), so
// a client on the host dials 127.0.0.1 and one elsewhere tunnels over SSH.

func singleHostEndpoints(t *testing.T, documentDB bool, states map[string]provisioner.ContainerState) *SingleHostEndpointService {
	t.Helper()
	h := newSingleHostHarness(t)
	inst, _ := h.svc.GetInstance("proj-docsvc001")
	inst.DocumentDB, inst.Host = documentDB, singleHostContainer
	if err := h.svc.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	docker := &gatewayStates{recordingDocker: h.docker, states: states}
	return NewSingleHostEndpointService(h.svc.store, docker)
}

func runningWithPorts(ports map[string]int) provisioner.ContainerState {
	return provisioner.ContainerState{Found: true, Running: true, HostPorts: ports}
}

func TestASingleHostProjectIsReachedOnTheHostsLoopback(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, map[string]provisioner.ContainerState{
		singleHostContainer: runningWithPorts(map[string]int{"5432": 32801}),
	})
	view, err := endpoints.Describe(context.Background(), "proj-docsvc001")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !view.SingleHost || !view.PublicOffered || !view.Enabled || !view.Available || view.Host != "127.0.0.1" || view.Port != 32801 {
		t.Fatalf("view %+v", view)
	}
	if view.Internal.Host != singleHostContainer || view.Internal.Port != 5432 || !strings.Contains(view.Internal.ConnectionString, "sslmode=disable") {
		t.Fatalf("internal %+v", view.Internal)
	}
	if view.RequireTLS || view.CACertificate != "" || view.MongoPort != 0 || view.Internal.MongoHost != "" {
		t.Fatalf("a Postgres project on one host has no TLS and no Mongo half: %+v", view)
	}
	if !strings.HasPrefix(view.Connection.AllowPlaintext, "postgresql://postgres@127.0.0.1:32801/appdb") {
		t.Fatalf("connection %+v", view.Connection)
	}
}

func TestASingleHostDocumentDBProjectShowsItsMongoPortAndGatewayCA(t *testing.T) {
	endpoints := singleHostEndpoints(t, true, map[string]provisioner.ContainerState{
		singleHostContainer: runningWithPorts(map[string]int{"5432": 32801, "10260": 32802}),
		provisioner.DocumentDBGatewayName("proj-docsvc001"): {Found: true, Running: true},
	})
	endpoints.gatewayCA = func(context.Context, string) ([]byte, error) { return []byte("PEM"), nil }
	view, err := endpoints.Describe(context.Background(), "proj-docsvc001")
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if view.MongoPort != 32802 || !view.MongoAvailable || view.CACertificate != "PEM" {
		t.Fatalf("view %+v", view)
	}
	if !strings.HasPrefix(view.Connection.MongoRequireTLS, "mongodb://postgres@127.0.0.1:32802/") ||
		view.Internal.MongoHost != singleHostContainer || view.Internal.MongoPort != 10260 {
		t.Fatalf("mongo %+v internal %+v", view.Connection, view.Internal)
	}
}

func TestAStoppedGatewayIsReportedNotAnswering(t *testing.T) {
	endpoints := singleHostEndpoints(t, true, map[string]provisioner.ContainerState{
		singleHostContainer: runningWithPorts(map[string]int{"5432": 32801, "10260": 32802}),
		provisioner.DocumentDBGatewayName("proj-docsvc001"): {Found: true},
	})
	endpoints.gatewayCA = func(context.Context, string) ([]byte, error) { return nil, errors.New("must not be read") }
	view, err := endpoints.Describe(context.Background(), "proj-docsvc001")
	if err != nil || view.MongoAvailable || view.MongoPort != 32802 || view.CACertificate != "" {
		t.Fatalf("view %+v err %v", view, err)
	}
}

func TestAPausedSingleHostProjectAnswersNowhere(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, map[string]provisioner.ContainerState{
		singleHostContainer: {Found: true},
	})
	view, err := endpoints.Describe(context.Background(), "proj-docsvc001")
	if err != nil || view.Available || view.Port != 0 {
		t.Fatalf("view %+v err %v", view, err)
	}
}

// Its ports are published by the host, not chosen in Studio.
func TestASingleHostEndpointCannotBeChanged(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, nil)
	if _, err := endpoints.SetPublic(context.Background(), "proj-docsvc001", false); !errors.Is(err, ErrDBEndpointUnsupported) {
		t.Fatalf("SetPublic err %v", err)
	}
	if _, err := endpoints.SetRequireTLS(context.Background(), "proj-docsvc001", true); !errors.Is(err, ErrDBEndpointUnsupported) {
		t.Fatalf("SetRequireTLS err %v", err)
	}
}

func TestTheSingleHostEndpointRefusesOtherProjects(t *testing.T) {
	endpoints := singleHostEndpoints(t, false, nil)
	if _, err := endpoints.Describe(context.Background(), "proj-missing"); err == nil {
		t.Fatal("described a missing project")
	}
	inst, _ := endpoints.instances.FindByProjectID("proj-docsvc001")
	inst.DeploymentMode = domain.ModeK8s
	if err := endpoints.instances.Update(inst); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoints.Describe(context.Background(), "proj-docsvc001"); !errors.Is(err, ErrDBEndpointUnsupported) {
		t.Fatalf("err %v", err)
	}
}
