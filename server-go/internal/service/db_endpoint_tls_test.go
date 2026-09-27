package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func (h *endpointHarness) cluster(t *testing.T) *unstructured.Unstructured {
	t.Helper()
	cluster, err := h.kube.GetCRD(context.Background(), k8s.CNPGClusterGVR, endpointNamespace, endpointProject+"-postgres")
	if err != nil {
		t.Fatalf("GetCRD: %v", err)
	}
	return cluster
}

func TestANewProjectsClusterRequiresTLS(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	if !k8s.ClusterRequiresTLS(h.cluster(t)) {
		t.Fatal("the default cluster must require TLS")
	}
}

func TestTurningRequireTLSOffRewritesTheClusterAndRecordsIt(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	view, err := h.svc.SetRequireTLS(context.Background(), endpointProject, false)
	if err != nil {
		t.Fatalf("SetRequireTLS(false): %v", err)
	}
	if view.RequireTLS {
		t.Fatal("the view still says TLS is required")
	}
	if k8s.ClusterRequiresTLS(h.cluster(t)) {
		t.Fatal("the cluster still requires TLS, so the setting and the database disagree")
	}
}

func TestTurningRequireTLSBackOnRestoresHostSSL(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()
	if _, err := h.svc.SetRequireTLS(ctx, endpointProject, false); err != nil {
		t.Fatalf("off: %v", err)
	}
	view, err := h.svc.SetRequireTLS(ctx, endpointProject, true)
	if err != nil {
		t.Fatalf("on: %v", err)
	}
	if !view.RequireTLS || !k8s.ClusterRequiresTLS(h.cluster(t)) {
		t.Fatalf("view.RequireTLS=%v cluster requires TLS=%v", view.RequireTLS, k8s.ClusterRequiresTLS(h.cluster(t)))
	}
}

func TestRequireTLSIsNotRecordedWhenTheClusterCannotBeUpdated(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.kube.UpdateCRDError = errors.New("apiserver said no")
	if _, err := h.svc.SetRequireTLS(context.Background(), endpointProject, false); err == nil {
		t.Fatal("SetRequireTLS must fail when the cluster was not updated")
	}
	view, err := h.svc.Describe(context.Background(), endpointProject)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !view.RequireTLS {
		t.Fatal("the setting changed although the database did not")
	}
}

func TestRequireTLSIsRefusedForAProjectThatIsNotActive(t *testing.T) {
	h := newEndpointHarness(t, "PAUSED")
	_, err := h.svc.SetRequireTLS(context.Background(), endpointProject, false)
	if !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("got %v, want ErrProjectNotActive", err)
	}
	if !k8s.ClusterRequiresTLS(h.cluster(t)) {
		t.Fatal("a refused change rewrote the cluster")
	}
}

type busyClaimer struct{}

func (busyClaimer) Claim(context.Context, string, ProjectOperation) (func(), bool, error) {
	return nil, false, nil
}

func TestRequireTLSIsRefusedWhileAnotherOperationHoldsTheProject(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.svc.claimer = busyClaimer{}
	_, err := h.svc.SetRequireTLS(context.Background(), endpointProject, false)
	if !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("got %v, want ErrProjectOperationRunning", err)
	}
	if !k8s.ClusterRequiresTLS(h.cluster(t)) {
		t.Fatal("a refused change rewrote the cluster")
	}
}

func TestTheInternalStringFollowsTheTLSSetting(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	ctx := context.Background()
	view, err := h.svc.Describe(ctx, endpointProject)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !strings.Contains(view.Internal.ConnectionString, "sslmode=require") {
		t.Fatalf("internal string while TLS is required: %q", view.Internal.ConnectionString)
	}
	view, err = h.svc.SetRequireTLS(ctx, endpointProject, false)
	if err != nil {
		t.Fatalf("SetRequireTLS: %v", err)
	}
	if !strings.Contains(view.Internal.ConnectionString, "sslmode=prefer") {
		t.Fatalf("internal string with plaintext allowed: %q", view.Internal.ConnectionString)
	}
}

func TestAFailedServiceCreateClosesTheIngressItOpened(t *testing.T) {
	h := newEndpointHarness(t, "ACTIVE")
	h.kube.EnsurePublicDBError = errors.New("apiserver said no")
	if _, err := h.svc.SetPublic(context.Background(), endpointProject, true); err == nil {
		t.Fatal("SetPublic must fail")
	}
	if ports, open := h.kube.PublicDBIngress[endpointNamespace+"/"+endpointProject]; open {
		t.Fatalf("outside traffic is still admitted on %v with no endpoint behind it", ports)
	}
}
