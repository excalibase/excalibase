package k8s

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func pullPolicyOf(t *testing.T, image string, reuse bool) corev1.PullPolicy {
	t.Helper()
	app := minimalApp()
	app.Image = image
	opts := testRenderOptions
	opts.ReusePinnedImages = reuse
	workload, err := RenderAppWorkload(testNamespace, app, newResolver(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return workload.Deployment.Spec.Template.Spec.Containers[0].ImagePullPolicy
}

// EXC-839: a digest is immutable, so with the switch on a node that holds it never asks the registry again.
func TestRenderAppWorkloadReusesADigestOnlyWhenAsked(t *testing.T) {
	digested := "ghcr.io/acme/web@sha256:" + strings.Repeat("a", 64)
	if got := pullPolicyOf(t, digested, true); got != corev1.PullIfNotPresent {
		t.Errorf("digest with the switch on = %q, want IfNotPresent", got)
	}
	if got := pullPolicyOf(t, digested, false); got != corev1.PullAlways {
		t.Errorf("digest with the switch off = %q, want Always as before", got)
	}
}

// A tag can move under a node's cached copy, so it is pulled every time even with the switch on.
func TestRenderAppWorkloadStillPullsATagAlways(t *testing.T) {
	if got := pullPolicyOf(t, "ghcr.io/acme/web:1.4.2", true); got != corev1.PullAlways {
		t.Errorf("tag with the switch on = %q, want Always", got)
	}
}
