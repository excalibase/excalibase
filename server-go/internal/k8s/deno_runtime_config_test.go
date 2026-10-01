package k8s

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const runtimeProvisioningURL = "http://provisioning.excalibase-platform.svc.cluster.local:24005"

func containerEnv(dep *appsv1.Deployment) map[string]string {
	out := map[string]string{}
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		out[env.Name] = env.Value
	}
	return out
}

// A project's runtime reaches provisioning for ctx.storage and the export
// metadata callback, and runs query/mutation/action functions (EXC-518).
func TestEnsureDenoRuntime_RuntimeReachesProvisioningAndRunsFunctions(t *testing.T) {
	c := newFakeClient()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", ProvisioningURL: runtimeProvisioningURL}
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, spec); err != nil {
		t.Fatal(err)
	}
	env := containerEnv(denoDeployment(t, c))
	if env["EXCALIBASE_PROVISIONING_URL"] != runtimeProvisioningURL {
		t.Fatalf("EXCALIBASE_PROVISIONING_URL = %q", env["EXCALIBASE_PROVISIONING_URL"])
	}
	if env["EXCALIBASE_FUNCTIONS_V2"] != "1" {
		t.Fatalf("EXCALIBASE_FUNCTIONS_V2 = %q, want 1", env["EXCALIBASE_FUNCTIONS_V2"])
	}
}

// A runtime created before the runtime knew where provisioning is gets the
// address on the next deploy; nothing else about it changes.
func TestEnsureDenoRuntime_ExistingRuntimeWithoutProvisioningURLIsUpdated(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	old := buildDenoDeployment(egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s"})
	old.Spec.Template.Spec.Containers[0].Env = old.Spec.Template.Spec.Containers[0].Env[:2] // RUNTIME_SECRET, ALLOWED_HOSTS only
	if _, err := c.clientset.AppsV1().Deployments(egressNS).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", ProvisioningURL: runtimeProvisioningURL}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	env := containerEnv(denoDeployment(t, c))
	if env["EXCALIBASE_PROVISIONING_URL"] != runtimeProvisioningURL || env["EXCALIBASE_FUNCTIONS_V2"] != "1" {
		t.Fatalf("existing runtime not brought up to date: %v", env)
	}
	if env["RUNTIME_SECRET"] != "s" || env["ALLOWED_HOSTS"] != "" {
		t.Fatalf("unrelated env changed: %v", env)
	}
	if n := countActions(c, "update", "deployments"); n != 1 {
		t.Fatalf("want one deployment update, got %d", n)
	}
	if n := countActions(c, "update", "networkpolicies"); n != 0 {
		t.Fatalf("an unchanged allowlist must not touch the egress policy, got %d updates", n)
	}
}

func TestEnsureDenoRuntime_UpToDateRuntimeIsANoop(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", ProvisioningURL: runtimeProvisioningURL, AllowedHosts: []string{"a.example.com"}}
	for i := 0; i < 2; i++ {
		if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
			t.Fatal(err)
		}
	}
	if countActions(c, "update", "deployments") != 0 || countActions(c, "update", "networkpolicies") != 0 {
		t.Fatal("an up-to-date runtime must not be touched")
	}
}
