package k8s

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A new runtime never starts from an image that can change under it (EXC-447).
func TestEnsureDenoRuntime_NewRuntimeRefusesAFloatingImage(t *testing.T) {
	for name, image := range map[string]string{
		"no image": "",
		"latest":   "excalibase/deno-runtime:latest",
		"moving":   "excalibase/deno-runtime:main",
		"untagged": "excalibase/deno-runtime",
	} {
		t.Run(name, func(t *testing.T) {
			c := newFakeClient()
			err := c.EnsureDenoRuntime(context.Background(), egressNS, DenoRuntimeSpec{Image: image, RuntimeSecret: "s"})
			if err == nil || !strings.Contains(err.Error(), "deno runtime image") {
				t.Fatalf("got %v, want a refusal naming the deno runtime image", err)
			}
			if _, getErr := c.clientset.AppsV1().Deployments(egressNS).Get(context.Background(), denoRuntimeName, metav1.GetOptions{}); getErr == nil {
				t.Fatal("a deployment was created from a floating image")
			}
			if n := countActions(c, "create", "secrets"); n != 0 {
				t.Fatalf("wrote %d secrets before refusing the image", n)
			}
		})
	}
}

func TestEnsureDenoRuntime_NewRuntimeAcceptsAReleaseTag(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, DenoRuntimeSpec{Image: "excalibase/deno-runtime:1.7.1", RuntimeSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if got := denoDeployment(t, c).Spec.Template.Spec.Containers[0].Image; got != "excalibase/deno-runtime:1.7.1" {
		t.Fatalf("runtime image %q", got)
	}
}
