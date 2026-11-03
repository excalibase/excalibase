package k8s

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEnsurePublicDBServiceCarriesTheAllowListAndNoNodePort(t *testing.T) {
	c := newFakeClient(cnpgReadWriteService())
	ctx := context.Background()
	spec := dbEndpointSpec()
	spec.SourceRanges = []string{"198.51.100.7/32", "2001:db8::/32"}
	spec.NoNodePorts = true

	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, spec); err != nil {
		t.Fatalf("EnsurePublicDBService: %v", err)
	}
	svc, _ := c.clientset.CoreV1().Services(dbEndpointNS).Get(ctx, dbEndpointSvcName, metav1.GetOptions{})
	if !slices.Equal(svc.Spec.LoadBalancerSourceRanges, spec.SourceRanges) {
		t.Fatalf("source ranges = %v", svc.Spec.LoadBalancerSourceRanges)
	}
	// A NodePort would answer on every node address without the list.
	if svc.Spec.AllocateLoadBalancerNodePorts == nil || *svc.Spec.AllocateLoadBalancerNodePorts {
		t.Fatalf("allocateLoadBalancerNodePorts = %v, want false", svc.Spec.AllocateLoadBalancerNodePorts)
	}
	ranges, err := c.PublicDBServiceSourceRanges(ctx, dbEndpointNS, dbEndpointSvcName)
	if err != nil || !slices.Equal(ranges, spec.SourceRanges) {
		t.Fatalf("read back %v, %v", ranges, err)
	}
}

func TestEnsurePublicDBServiceWithoutTheFlagRendersAsBefore(t *testing.T) {
	c := newFakeClient(cnpgReadWriteService())
	ctx := context.Background()
	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, dbEndpointSpec()); err != nil {
		t.Fatalf("EnsurePublicDBService: %v", err)
	}
	svc, _ := c.clientset.CoreV1().Services(dbEndpointNS).Get(ctx, dbEndpointSvcName, metav1.GetOptions{})
	if svc.Spec.LoadBalancerSourceRanges != nil || svc.Spec.AllocateLoadBalancerNodePorts != nil {
		t.Fatalf("ranges %v, node ports %v: want both unset", svc.Spec.LoadBalancerSourceRanges, svc.Spec.AllocateLoadBalancerNodePorts)
	}
}

func TestEnsurePublicDBServiceNarrowsAndWidensAnExistingService(t *testing.T) {
	c := newFakeClient(cnpgReadWriteService())
	ctx := context.Background()
	spec := dbEndpointSpec()
	spec.NoNodePorts = true
	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, spec); err != nil {
		t.Fatalf("create: %v", err)
	}
	spec.SourceRanges = []string{"203.0.113.0/24"}
	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, spec); err != nil {
		t.Fatalf("narrow: %v", err)
	}
	if ranges, _ := c.PublicDBServiceSourceRanges(ctx, dbEndpointNS, dbEndpointSvcName); !slices.Equal(ranges, spec.SourceRanges) {
		t.Fatalf("narrowed to %v", ranges)
	}
	spec.SourceRanges = nil
	if err := c.EnsurePublicDBService(ctx, dbEndpointNS, spec); err != nil {
		t.Fatalf("widen: %v", err)
	}
	if ranges, _ := c.PublicDBServiceSourceRanges(ctx, dbEndpointNS, dbEndpointSvcName); ranges != nil {
		t.Fatalf("widened to %v, want none", ranges)
	}
}

func TestPublicDBServiceSourceRangesOfAMissingServiceIsAnError(t *testing.T) {
	c := newFakeClient()
	if _, err := c.PublicDBServiceSourceRanges(context.Background(), dbEndpointNS, dbEndpointSvcName); err == nil {
		t.Fatal("a missing Service has no ranges to confirm")
	}
}
