package service

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// A database container the engine restarts gets a new network namespace; its
// gateway keeps the old one and serves nothing until it restarts too (EXC-576).

type gatewayStates struct {
	*recordingDocker
	states map[string]provisioner.ContainerState
}

func (g *gatewayStates) ContainerState(_ context.Context, id string) (provisioner.ContainerState, error) {
	return g.states[id], nil
}

func newGatewayWatchHarness(t *testing.T, gatewayStarted time.Time) (*ProvisioningService, *gatewayStates) {
	t.Helper()
	h := newSingleHostHarness(t)
	databaseStarted := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	docker := &gatewayStates{recordingDocker: h.docker, states: map[string]provisioner.ContainerState{
		singleHostContainer: {Found: true, Running: true, StartedAt: databaseStarted},
		provisioner.DocumentDBGatewayName("proj-docsvc001"): {Found: true, Running: true, StartedAt: gatewayStarted},
	}}
	h.svc.SetDockerClient(docker)
	return h.svc, docker
}

func gatewayRestarted(docker *gatewayStates) bool {
	return slices.Contains(docker.calls, "start:"+provisioner.DocumentDBGatewayName("proj-docsvc001"))
}

func TestTheWatchRestartsAGatewayOlderThanItsDatabase(t *testing.T) {
	svc, docker := newGatewayWatchHarness(t, time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC))
	if err := svc.RefreshDocumentDBGateways(context.Background()); err != nil {
		t.Fatalf("RefreshDocumentDBGateways: %v", err)
	}
	if !gatewayRestarted(docker) {
		t.Fatalf("stale gateway left alone: %v", docker.calls)
	}
}

func TestTheWatchLeavesAFreshGatewayAlone(t *testing.T) {
	svc, docker := newGatewayWatchHarness(t, time.Date(2026, 10, 10, 12, 0, 1, 0, time.UTC))
	if err := svc.RefreshDocumentDBGateways(context.Background()); err != nil {
		t.Fatalf("RefreshDocumentDBGateways: %v", err)
	}
	if len(docker.calls) != 0 {
		t.Fatalf("touched a fresh gateway: %v", docker.calls)
	}
}

// A paused project, or one another operation holds, is not the watch's to start.
func TestTheWatchSkipsProjectsThatAreNotActiveOrAreBusy(t *testing.T) {
	stale := time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC)
	svc, docker := newGatewayWatchHarness(t, stale)
	inst, _ := svc.GetInstance("proj-docsvc001")
	inst.Status = "PAUSED"
	if err := svc.store.Update(inst); err != nil {
		t.Fatal(err)
	}
	if err := svc.RefreshDocumentDBGateways(context.Background()); err != nil || gatewayRestarted(docker) {
		t.Fatalf("paused project: err %v calls %v", err, docker.calls)
	}

	svc, docker = newGatewayWatchHarness(t, stale)
	release, claimed, err := svc.claimer().Claim(context.Background(), "proj-docsvc001", OperationPause)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	if err := svc.RefreshDocumentDBGateways(context.Background()); err != nil || gatewayRestarted(docker) {
		t.Fatalf("busy project: err %v calls %v", err, docker.calls)
	}
}

func TestTheWatchReportsAProjectWithoutAGateway(t *testing.T) {
	svc, docker := newGatewayWatchHarness(t, time.Time{})
	delete(docker.states, provisioner.DocumentDBGatewayName("proj-docsvc001"))
	if err := svc.RefreshDocumentDBGateways(context.Background()); err == nil {
		t.Fatal("a missing gateway went unreported")
	}
}

func TestTheWatchIgnoresKubernetesAndPlainPostgresProjects(t *testing.T) {
	svc, docker := newGatewayWatchHarness(t, time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC))
	inst, _ := svc.GetInstance("proj-docsvc001")
	for _, mutate := range []func(*domain.DatabaseInstance){
		func(i *domain.DatabaseInstance) { i.DeploymentMode = domain.ModeK8s },
		func(i *domain.DatabaseInstance) { i.DeploymentMode, i.DocumentDB = domain.ModeDocker, false },
	} {
		mutate(inst)
		if err := svc.store.Update(inst); err != nil {
			t.Fatal(err)
		}
		if err := svc.RefreshDocumentDBGateways(context.Background()); err != nil || gatewayRestarted(docker) {
			t.Fatalf("%+v: err %v calls %v", inst, err, docker.calls)
		}
	}
}

func TestTheWatchNeedsAnEngine(t *testing.T) {
	svc := NewProvisioningService(documentDBStore(t), provisioner.NewFactory(), nil)
	if err := svc.RefreshDocumentDBGateways(context.Background()); !errors.Is(err, ErrNoContainerEngine) {
		t.Fatalf("err = %v", err)
	}
}

// Only the leading replica restarts gateways, so two never race one.
func TestOnlyTheLeaderRunsTheWatch(t *testing.T) {
	stale := time.Date(2026, 10, 10, 11, 0, 0, 0, time.UTC)
	svc, docker := newGatewayWatchHarness(t, stale)
	svc.refreshGatewaysIfLeader(context.Background(), fakeLeader{leader: false})
	svc.refreshGatewaysIfLeader(context.Background(), fakeLeader{leader: true, err: errors.New("lock store down")})
	if gatewayRestarted(docker) {
		t.Fatalf("a follower restarted a gateway: %v", docker.calls)
	}
	svc.refreshGatewaysIfLeader(context.Background(), fakeLeader{leader: true})
	if !gatewayRestarted(docker) {
		t.Fatalf("the leader left a stale gateway: %v", docker.calls)
	}
}
