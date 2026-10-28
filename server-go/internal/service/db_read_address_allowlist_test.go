package service

import (
	"context"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-666 with EXC-665: the read port admits exactly what the public port admits.

func (h *readHarness) readerRanges(t *testing.T) ([]string, bool) {
	t.Helper()
	spec, ok := h.kube.PublicDBServices[readerServiceKey()]
	if !ok {
		t.Fatal("no reader Service")
	}
	return spec.SourceRanges, spec.NoNodePorts
}

func TestReadAddress_OpensWithTheAllowList(t *testing.T) {
	h := newReadHarness(t, 3, true)
	lists := h.withAllowList(true)
	if _, err := lists.SetDBSourceAllowList(context.Background(), withProject(mustAllowList(t, false, "203.0.113.0/24"))); err != nil {
		t.Fatal(err)
	}
	h.openPublic(t)
	h.enable(t)
	ranges, noNodePorts := h.readerRanges(t)
	if !slices.Equal(ranges, []string{"203.0.113.0/24"}) || !noNodePorts {
		t.Fatalf("reader ranges %v nodePortsOff=%v", ranges, noNodePorts)
	}
}

func TestReadAddress_FollowsAnAllowListChange(t *testing.T) {
	h := newReadHarness(t, 3, true)
	lists := h.withAllowList(true)
	if _, err := lists.SetDBSourceAllowList(context.Background(), withProject(mustAllowList(t, true))); err != nil {
		t.Fatal(err)
	}
	h.openPublic(t)
	h.enable(t)
	if ranges, _ := h.readerRanges(t); len(ranges) != 0 {
		t.Fatalf("allow any rendered ranges %v", ranges)
	}
	if _, err := h.svc.SetAllowedSources(context.Background(), endpointProject, mustAllowList(t, false, "198.51.100.7")); err != nil {
		t.Fatalf("SetAllowedSources: %v", err)
	}
	ranges, noNodePorts := h.readerRanges(t)
	if !slices.Equal(ranges, []string{"198.51.100.7/32"}) || !noNodePorts {
		t.Fatalf("after the change the reader admits %v nodePortsOff=%v", ranges, noNodePorts)
	}
	primary, _ := h.publicSpecRanges(t)
	if !slices.Equal(primary, ranges) {
		t.Fatalf("primary %v, reader %v", primary, ranges)
	}
}

func withProject(list domain.DBSourceAllowList) domain.DBSourceAllowList {
	list.ProjectID = endpointProject
	return list
}
