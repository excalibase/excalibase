package k8s

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	runtimeDBURL        = "postgres://owner:s3cret-pass@proj-rw.egress-ns.svc.cluster.local:5432/app?sslmode=require"
	runtimeDBURLRotated = "postgres://owner:rotated-pass@proj-rw.egress-ns.svc.cluster.local:5432/app?sslmode=require"
)

func runtimeDBEnv(t *testing.T, dep *appsv1.Deployment) corev1.EnvVar {
	t.Helper()
	for _, env := range dep.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "EXCALIBASE_DB_URL" {
			return env
		}
	}
	t.Fatalf("runtime has no EXCALIBASE_DB_URL: %v", dep.Spec.Template.Spec.Containers[0].Env)
	return corev1.EnvVar{}
}

func runtimeDBSecret(t *testing.T, c *Client) *corev1.Secret {
	t.Helper()
	secret, err := c.clientset.CoreV1().Secrets(egressNS).Get(context.Background(), "deno-runtime-db", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("runtime database secret: %v", err)
	}
	return secret
}

// ctx.db opens its pool from the runtime's own environment, so the runtime is
// given the project's URL, by reference to a Secret and never as a literal (EXC-625).
func TestEnsureDenoRuntime_DatabaseURLIsASecretReference(t *testing.T) {
	c := newFakeClient()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", DatabaseURL: runtimeDBURL}
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, spec); err != nil {
		t.Fatal(err)
	}
	dep := denoDeployment(t, c)
	env := runtimeDBEnv(t, dep)
	if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("EXCALIBASE_DB_URL must come from a Secret, got %+v", env)
	}
	ref := env.ValueFrom.SecretKeyRef
	if ref.Name != "deno-runtime-db" || ref.Key != "EXCALIBASE_DB_URL" {
		t.Fatalf("secret reference = %s/%s", ref.Name, ref.Key)
	}
	if got := string(runtimeDBSecret(t, c).Data["EXCALIBASE_DB_URL"]); got != runtimeDBURL {
		t.Fatalf("secret holds %q, want %q", got, runtimeDBURL)
	}
	if rendered := dep.String(); strings.Contains(rendered, "s3cret-pass") {
		t.Fatal("the database password must not appear anywhere in the Deployment")
	}
}

// Without a database login nothing is rendered: ctx.db then says so itself.
func TestEnsureDenoRuntime_NoDatabaseURLNoSecret(t *testing.T) {
	c := newFakeClient()
	if err := c.EnsureDenoRuntime(context.Background(), egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := containerEnv(denoDeployment(t, c))["EXCALIBASE_DB_URL"]; ok {
		t.Fatal("no database login, no EXCALIBASE_DB_URL")
	}
	if n := countActions(c, "create", "secrets"); n != 0 {
		t.Fatalf("no database login, no secret; got %d creates", n)
	}
}

// A runtime created before EXC-625 gets the reference on the next deploy.
func TestEnsureDenoRuntime_ExistingRuntimeGetsTheDatabaseURL(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	old := buildDenoDeployment(egressNS, DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s"})
	if _, err := c.clientset.AppsV1().Deployments(egressNS).Create(ctx, old, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", DatabaseURL: runtimeDBURL}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if env := runtimeDBEnv(t, denoDeployment(t, c)); env.ValueFrom == nil || env.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("existing runtime not given the reference: %+v", env)
	}
	if got := string(runtimeDBSecret(t, c).Data["EXCALIBASE_DB_URL"]); got != runtimeDBURL {
		t.Fatalf("secret holds %q", got)
	}
}

// A rotated password rewrites the Secret and rolls the pod, which only reads
// its environment at start; the same URL again touches nothing.
func TestEnsureDenoRuntime_RotatedDatabaseURLRollsTheRuntime(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", DatabaseURL: runtimeDBURL}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	before := denoDeployment(t, c).Spec.Template.Annotations["excalibase.io/db-url-hash"]
	if before == "" {
		t.Fatal("the pod template must carry the database URL's fingerprint")
	}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if countActions(c, "update", "deployments") != 0 || countActions(c, "update", "secrets") != 0 {
		t.Fatal("an unchanged URL must not touch the runtime")
	}

	spec.DatabaseURL = runtimeDBURLRotated
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	if got := string(runtimeDBSecret(t, c).Data["EXCALIBASE_DB_URL"]); got != runtimeDBURLRotated {
		t.Fatalf("secret not rotated: %q", got)
	}
	after := denoDeployment(t, c).Spec.Template.Annotations["excalibase.io/db-url-hash"]
	if after == before {
		t.Fatal("a rotated URL must change the pod template so the runtime restarts")
	}
	if strings.Contains(after, "rotated-pass") {
		t.Fatal("the fingerprint must not carry the password")
	}
}

// A project whose database login is gone keeps no login in its namespace.
func TestEnsureDenoRuntime_LoginGoneRemovesTheSecretAndReference(t *testing.T) {
	c := newFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: egressImage, RuntimeSecret: "s", DatabaseURL: runtimeDBURL}
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	spec.DatabaseURL = ""
	if err := c.EnsureDenoRuntime(ctx, egressNS, spec); err != nil {
		t.Fatal(err)
	}
	dep := denoDeployment(t, c)
	if _, ok := containerEnv(dep)["EXCALIBASE_DB_URL"]; ok {
		t.Fatal("the reference must go with the login")
	}
	if _, ok := dep.Spec.Template.Annotations["excalibase.io/db-url-hash"]; ok {
		t.Fatal("the fingerprint must go with the login")
	}
	if _, err := c.clientset.CoreV1().Secrets(egressNS).Get(ctx, "deno-runtime-db", metav1.GetOptions{}); err == nil {
		t.Fatal("the secret must be deleted")
	}
}

// A runtime made from an older image moves to the configured one, its fence
// with it: a runtime fix (EXC-625's pool options) otherwise never reaches a
// project whose runtime already exists.
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
