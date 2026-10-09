package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// roomyNodes is a cluster of n untainted nodes, each with room for any tier's instance.
func roomyNodes(n int) k8s.ClusterCapacity {
	capacity := k8s.ClusterCapacity{}
	for i := range n {
		capacity.Nodes = append(capacity.Nodes, k8s.NodeCapacity{
			Name: fmt.Sprintf("node-%d", i), AllocatableCPUMilli: 32000, AllocatableMemBytes: 64 << 30,
		})
		capacity.AllocatableCPUMilli += 32000
		capacity.AllocatableMemBytes += 64 << 30
	}
	return capacity
}

func provisionIn(svc *ProvisioningService, orgID string) (*domain.ProvisioningResponse, error) {
	return svc.Provision(context.Background(), domain.ProvisioningRequest{
		PostgresVersion: "17", ProjectName: "spread", OrgID: orgID, DBType: domain.PostgreSQL,
	})
}

func assertTooFewNodes(t *testing.T, err error, instances, nodes int) {
	t.Helper()
	var spread *NotEnoughNodesError
	if !errors.As(err, &spread) {
		t.Fatalf("err = %v, want NotEnoughNodesError", err)
	}
	if spread.Instances != instances || spread.Nodes != nodes {
		t.Errorf("refusal = %+v, want %d instances / %d nodes", spread, instances, nodes)
	}
	if !strings.Contains(err.Error(), "own node") {
		t.Errorf("the refusal must say why: %q", err.Error())
	}
}

func TestProvision_StandardIsRefusedOnTooFewNodes(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	mock.Capacity = roomyNodes(1)

	_, err := provisionIn(svc, "org1")
	assertTooFewNodes(t, err, 3, 1)
	if slices.ContainsFunc(mock.Calls, func(call string) bool { return strings.HasPrefix(call, "CreateNamespace") }) || len(mock.CRDs) != 0 {
		t.Errorf("nothing may be created for a cluster that cannot spread: calls=%v", mock.Calls)
	}
}

func TestProvision_StandardSpreadsOnThreeNodes(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	mock.Capacity = roomyNodes(3)

	if _, err := provisionIn(svc, "org1"); err != nil {
		t.Fatalf("Provision: %v", err)
	}
}

func TestProvision_TaintedNodesCannotHoldAnInstance(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	mock.Capacity = roomyNodes(3)
	mock.Capacity.Nodes[0].Tainted = true

	_, err := provisionIn(svc, "org1")
	assertTooFewNodes(t, err, 3, 2)
}

func TestProvision_EachInstanceMustFitOnItsOwnNode(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	mock.Capacity = roomyNodes(3)
	mock.Capacity.Nodes[2].RequestedCPUMilli = 31000

	_, err := provisionIn(svc, "org1")
	var spread *NotEnoughNodesError
	if err == nil || errors.As(err, &spread) || !strings.Contains(err.Error(), "not enough capacity") {
		t.Fatalf("three nodes exist but one is full: want the capacity refusal, got %v", err)
	}
}

func TestProvision_UnreadableNodesRefuseAMultiInstanceTier(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	setOrgTier(svc, "org1", domain.Standard)
	mock.CapacityError = errors.New("apiserver down")

	if _, err := provisionIn(svc, "org1"); !errors.Is(err, ErrNodePlacementUnknown) {
		t.Fatalf("err = %v, want ErrNodePlacementUnknown", err)
	}
}

func TestProvision_FreeNeedsNoSecondNode(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.Capacity = roomyNodes(1)

	if _, err := provisionIn(svc, "org1"); err != nil {
		t.Fatalf("a single-instance tier runs on one node: %v", err)
	}
}

func TestRestorePlanRefusesATierTheNodesCannotSpread(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	setOrgTier(svc, "org", domain.Enterprise)
	mock.Capacity = roomyNodes(3)

	_, err := svc.RestorePlan(context.Background(), sourceInstance())
	assertTooFewNodes(t, err, 5, 3)
}

func TestEnsureOrgCanTakeProjectRefusesATierTheNodesCannotSpread(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	setOrgTier(svc, "org", domain.Standard)
	mock.Capacity = roomyNodes(2)

	assertTooFewNodes(t, svc.EnsureOrgCanTakeProject(context.Background(), "org"), 3, 2)
}

func TestRequireNodeSpread(t *testing.T) {
	svc, _, mock := setupProvisioningTest(t)
	mock.Capacity = roomyNodes(3)
	ctx := context.Background()

	standard, _ := config.GetTierConfig(domain.Standard)
	enterprise, _ := config.GetTierConfig(domain.Enterprise)
	free, _ := config.GetTierConfig(domain.Free)

	if err := svc.RequireNodeSpread(ctx, domain.Standard, standard); err != nil {
		t.Errorf("3 instances on 3 nodes: %v", err)
	}
	assertTooFewNodes(t, svc.RequireNodeSpread(ctx, domain.Enterprise, enterprise), 5, 3)
	mock.CapacityError = errors.New("down")
	if err := svc.RequireNodeSpread(ctx, domain.Free, free); err != nil {
		t.Errorf("one instance needs no node lookup: %v", err)
	}
}

// ADR 0038: a single Docker/Podman host is one node. A tier with more copies
// is refused before anything is created, as on a cluster too small for it.
func TestRequireNodeCount_SingleHostIsOneNode(t *testing.T) {
	svc := NewProvisioningService(nil, nil, nil)
	assertTooFewNodes(t, svc.RequireNodeCount(context.Background(), domain.Standard, 3), 3, 1)
	if err := svc.RequireNodeCount(context.Background(), domain.Free, 1); err != nil {
		t.Fatalf("one copy on one host: %v", err)
	}
}
