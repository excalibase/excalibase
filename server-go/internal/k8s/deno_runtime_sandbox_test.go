package k8s

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func denoDeploymentOf(t *testing.T, c *Client, namespace string) *runtimeClassView {
	t.Helper()
	dep, err := c.clientset.AppsV1().Deployments(namespace).Get(context.Background(), denoRuntimeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("deno deployment: %v", err)
	}
	return &runtimeClassView{name: dep.Spec.Template.Spec.RuntimeClassName}
}

type runtimeClassView struct{ name *string }

func TestEnsureDenoRuntime_RunsUnderTheSandboxRuntimeClass(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureDenoRuntime(context.Background(), "proj-sandbox", DenoRuntimeSpec{Tier: "FREE", RuntimeClass: "gvisor-custom"}); err != nil {
		t.Fatalf("EnsureDenoRuntime: %v", err)
	}
	got := denoDeploymentOf(t, c, "proj-sandbox").name
	if got == nil || *got != "gvisor-custom" {
		t.Fatalf("RuntimeClassName = %v, want gvisor-custom", got)
	}
}

func TestEnsureDenoRuntime_NeverRunsUnsandboxedWhenNoClassIsNamed(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureDenoRuntime(context.Background(), "proj-default", DenoRuntimeSpec{Tier: "FREE"}); err != nil {
		t.Fatalf("EnsureDenoRuntime: %v", err)
	}
	got := denoDeploymentOf(t, c, "proj-default").name
	if got == nil || *got == "" {
		t.Fatalf("a runtime with no class named must still be sandboxed, got %v", got)
	}
}

func TestEnsureDenoRuntime_ExistingUnsandboxedRuntimeMovesIntoTheSandbox(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Tier: "FREE", RuntimeClass: "gvisor"}
	if err := c.EnsureDenoRuntime(ctx, "proj-old", spec); err != nil {
		t.Fatalf("EnsureDenoRuntime: %v", err)
	}
	dep, _ := c.clientset.AppsV1().Deployments("proj-old").Get(ctx, denoRuntimeName, metav1.GetOptions{})
	dep.Spec.Template.Spec.RuntimeClassName = nil
	if _, err := c.clientset.AppsV1().Deployments("proj-old").Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, "proj-old", spec); err != nil {
		t.Fatalf("EnsureDenoRuntime again: %v", err)
	}
	got := denoDeploymentOf(t, c, "proj-old").name
	if got == nil || *got != "gvisor" {
		t.Fatalf("RuntimeClassName = %v, want the runtime moved into gvisor", got)
	}
}
