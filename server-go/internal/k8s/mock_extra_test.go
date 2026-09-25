package k8s

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestMockClient_NamespaceAndCRDListing(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	_ = m.CreateProjectNamespace(ctx, "org-a-p1", "org-a")
	_ = m.CreateProjectNamespace(ctx, "org-a-p2", "org-a")
	_ = m.CreateProjectNamespace(ctx, "other", "org-a")

	got, err := m.ListNamespaces(ctx, "org-a-")
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("prefix filter: got %d, want 2 (%v)", len(got), got)
	}

	gvr := schema.GroupVersionResource{Group: "test.io", Version: "v1", Resource: "widgets"}
	obj := &unstructured.Unstructured{}
	// The mock's ListCRDs keys by namespace/<resource>, so name the object to
	// match the GVR resource for the lookup to hit.
	obj.SetName(gvr.Resource)
	if err := m.ApplyCRD(ctx, gvr, "org-a-p1", obj); err != nil {
		t.Fatalf("ApplyCRD: %v", err)
	}
	crds, err := m.ListCRDs(ctx, gvr, "org-a-p1")
	if err != nil {
		t.Fatalf("ListCRDs: %v", err)
	}
	if len(crds) != 1 {
		t.Errorf("ListCRDs: got %d, want 1", len(crds))
	}
	// Unknown namespace → empty.
	if empty, _ := m.ListCRDs(ctx, gvr, "nope"); len(empty) != 0 {
		t.Errorf("ListCRDs unknown ns should be empty, got %d", len(empty))
	}
}

func TestMockClient_HelmAndDenoAndCapacity(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()

	if err := m.InstallHelmChart(ctx, "ns", "rel", "/chart", map[string]interface{}{"x": 1}); err != nil {
		t.Fatalf("InstallHelmChart: %v", err)
	}
	if _, ok := m.HelmReleases["ns/rel"]; !ok {
		t.Error("helm release not recorded")
	}
	if err := m.UninstallHelmChart(ctx, "ns", "rel"); err != nil {
		t.Fatalf("UninstallHelmChart: %v", err)
	}
	if _, ok := m.HelmReleases["ns/rel"]; ok {
		t.Error("helm release not removed")
	}

	if err := m.EnsureDenoRuntime(ctx, "proj", DenoRuntimeSpec{Tier: "FREE"}); err != nil {
		t.Fatalf("EnsureDenoRuntime: %v", err)
	}
	if !m.DenoRuntimes["proj"] {
		t.Error("deno runtime not recorded")
	}

	if exists, err := m.GetDeployment(ctx, "ns", "dep"); err != nil || !exists {
		t.Errorf("GetDeployment mock should report exists, got %v/%v", exists, err)
	}
	if err := m.ApplyManifestURL(ctx, "https://example/manifest.yaml"); err != nil {
		t.Errorf("ApplyManifestURL: %v", err)
	}

	m.Capacity = ClusterCapacity{AllocatableCPUMilli: 4000}
	cap, err := m.GetClusterCapacity(ctx)
	if err != nil || cap.AllocatableCPUMilli != 4000 {
		t.Errorf("GetClusterCapacity: got %+v err=%v", cap, err)
	}
}

func TestMockClient_ErrorInjection(t *testing.T) {
	m := NewMockClient()
	ctx := context.Background()
	m.HelmError = errors.New("helm boom")
	if err := m.InstallHelmChart(ctx, "ns", "rel", "/chart", nil); err == nil {
		t.Error("expected injected helm error")
	}
	m.EnsureDenoError = errors.New("deno boom")
	if err := m.EnsureDenoRuntime(ctx, "p", DenoRuntimeSpec{}); err == nil {
		t.Error("expected injected deno error")
	}
	m.CapacityError = errors.New("cap boom")
	if _, err := m.GetClusterCapacity(ctx); err == nil {
		t.Error("expected injected capacity error")
	}
}
