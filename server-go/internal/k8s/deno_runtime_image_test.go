package k8s

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A runtime made from an older image moves to the configured one, its fence
// with it: a runtime fix otherwise never reaches a project whose runtime
// already exists (EXC-625).
func TestEnsureDenoRuntime_ExistingRuntimeMovesToTheConfiguredImage(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	old := buildDenoDeployment(egressNS, DenoRuntimeSpec{Image: "excalibase/deno-runtime:old", RuntimeSecret: "s"})
	if _, err := c.clientset.AppsV1().Deployments(egressNS).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	pod := denoDeployment(t, c).Spec.Template.Spec
	if pod.Containers[0].Image != egressImage || pod.InitContainers[0].Image != egressImage {
		t.Fatalf("runtime image %q, fence image %q, want %q", pod.Containers[0].Image, pod.InitContainers[0].Image, egressImage)
	}
	if n := countActions(c, "update", "deployments"); n != 1 {
		t.Fatalf("want one deployment update, got %d", n)
	}
}

// A caller that names no image leaves the runtime's image alone.
func TestEnsureDenoRuntime_NoImageNamedKeepsTheRuntimeImage(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	old := buildDenoDeployment(egressNS, DenoRuntimeSpec{Image: "excalibase/deno-runtime:old", RuntimeSecret: "s"})
	if _, err := c.clientset.AppsV1().Deployments(egressNS).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{RuntimeSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if got := denoDeployment(t, c).Spec.Template.Spec.Containers[0].Image; got != "excalibase/deno-runtime:old" {
		t.Fatalf("image changed to %q", got)
	}
}
