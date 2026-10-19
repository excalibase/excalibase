package k8s

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The runtime runs customer code, so neither it nor its fence may hold a
// privilege it does not use (EXC-642, Kubescape on the live cluster).
func assertUnprivileged(t *testing.T, container corev1.Container) {
	t.Helper()
	ctx := container.SecurityContext
	if ctx == nil {
		t.Fatalf("%s: no securityContext", container.Name)
	}
	if ctx.AllowPrivilegeEscalation == nil || *ctx.AllowPrivilegeEscalation {
		t.Errorf("%s: privilege escalation allowed", container.Name)
	}
	if ctx.Capabilities == nil || len(ctx.Capabilities.Drop) != 1 || ctx.Capabilities.Drop[0] != "ALL" || len(ctx.Capabilities.Add) != 0 {
		t.Errorf("%s: capabilities = %+v, want every one dropped", container.Name, ctx.Capabilities)
	}
	if ctx.RunAsNonRoot == nil || !*ctx.RunAsNonRoot || ctx.RunAsUser == nil || *ctx.RunAsUser != denoRuntimeUID {
		t.Errorf("%s: must run as the image's deno user %d", container.Name, denoRuntimeUID)
	}
	if ctx.SeccompProfile == nil || ctx.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("%s: seccomp profile is not RuntimeDefault", container.Name)
	}
}

func runtimeSecretEnvOf(t *testing.T, dep *appsv1.Deployment) corev1.EnvVar {
	t.Helper()
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "RUNTIME_SECRET" {
			return env
		}
	}
	t.Fatal("RUNTIME_SECRET is not set")
	return corev1.EnvVar{}
}

// The secret is read from a Secret, so it is not in the pod spec that anything
// allowed to list pods (the log collector among them) can read.
func assertSecretByReference(t *testing.T, c *Client, dep *appsv1.Deployment, want string) {
	t.Helper()
	env := runtimeSecretEnvOf(t, dep)
	if env.Value != "" {
		t.Fatal("RUNTIME_SECRET is a literal in the pod spec")
	}
	ref := env.ValueFrom
	if ref == nil || ref.SecretKeyRef == nil || ref.SecretKeyRef.Name != denoRuntimeSecretName || ref.SecretKeyRef.Key != denoRuntimeSecretKey {
		t.Fatalf("RUNTIME_SECRET = %+v, want a reference to %s/%s", env, denoRuntimeSecretName, denoRuntimeSecretKey)
	}
	secret, err := c.clientset.CoreV1().Secrets(egressNS).Get(context.Background(), denoRuntimeSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("runtime Secret: %v", err)
	}
	if got := string(secret.Data[denoRuntimeSecretKey]); got != want {
		t.Fatalf("runtime Secret holds %q, want %q", got, want)
	}
}

func TestEnsureDenoRuntime_NewRuntimeIsUnprivilegedAndReadsItsSecretFromASecret(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	dep := denoDeployment(t, c)
	assertUnprivileged(t, dep.Spec.Template.Spec.Containers[0])
	assertUnprivileged(t, dep.Spec.Template.Spec.InitContainers[0])
	assertSecretByReference(t, c, dep, "s")
}

// A runtime made before EXC-642 (secret literal in its env, no securityContext)
// is brought up to date on the next deploy.
func TestEnsureDenoRuntime_ExistingRuntimeIsHardenedAndItsSecretMoved(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", ProvisioningURL: runtimeProvisioningURL}
	old := buildDenoDeployment(egressNS, spec)
	old.Spec.Template.Spec.Containers[0].SecurityContext = nil
	old.Spec.Template.Spec.InitContainers[0] = denoFenceContainer(egressImage, "")
	old.Spec.Template.Spec.InitContainers[0].SecurityContext.RunAsNonRoot = nil
	old.Spec.Template.Spec.InitContainers[0].SecurityContext.RunAsUser = nil
	old.Spec.Template.Spec.InitContainers[0].SecurityContext.SeccompProfile = nil
	for i, env := range old.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "RUNTIME_SECRET" {
			old.Spec.Template.Spec.Containers[0].Env[i] = corev1.EnvVar{Name: "RUNTIME_SECRET", Value: "s"}
		}
	}
	if _, err := c.clientset.AppsV1().Deployments(egressNS).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	dep := denoDeployment(t, c)
	assertUnprivileged(t, dep.Spec.Template.Spec.Containers[0])
	assertUnprivileged(t, dep.Spec.Template.Spec.InitContainers[0])
	assertSecretByReference(t, c, dep, "s")
	if n := countActions(c, "update", "deployments"); n != 1 {
		t.Fatalf("want one deployment update, got %d", n)
	}
}

// A changed master secret reaches a running runtime: the Secret changes and
// the pod template with it, so the runtime restarts holding the new value.
func TestEnsureDenoRuntime_ChangedSecretRollsTheRuntime(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "old"}); err != nil {
		t.Fatal(err)
	}
	before := denoDeployment(t, c).Spec.Template.Annotations[denoRuntimeSecretHashAnnotation]
	if err := c.EnsureDenoRuntime(ctx, egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "new"}); err != nil {
		t.Fatal(err)
	}
	dep := denoDeployment(t, c)
	assertSecretByReference(t, c, dep, "new")
	if after := dep.Spec.Template.Annotations[denoRuntimeSecretHashAnnotation]; after == "" || after == before {
		t.Fatalf("pod template secret hash %q -> %q, want a change", before, after)
	}
}

func TestEnsureDenoRuntime_UnchangedSecretTouchesNothing(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s"}
	for i := 0; i < 2; i++ {
		if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
			t.Fatal(err)
		}
	}
	if n := countActions(c, "update", "secrets"); n != 0 {
		t.Fatalf("an unchanged secret was rewritten %d times", n)
	}
}
