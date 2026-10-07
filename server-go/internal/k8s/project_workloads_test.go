package k8s

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

func listIngressNames(t *testing.T, c *Client) []string {
	t.Helper()
	list, err := c.clientset.NetworkingV1().Ingresses(testNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list ingresses: %v", err)
	}
	return namesOf(list.Items)
}

func TestWithdrawProjectWorkloads_RemovesEveryAppRouteAndScalesEverythingToZero(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.SyncAppDomains(ctx, testNamespace, app, []string{"shop.example.com"}, testDomainOptions); err != nil {
		t.Fatalf("sync domains: %v", err)
	}
	foreign := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "db-public", Namespace: testNamespace}}
	if _, err := c.clientset.NetworkingV1().Ingresses(testNamespace).Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.EnsureDenoRuntime(ctx, testNamespace, DenoRuntimeSpec{Image: "deno:test", RuntimeSecret: "s"}); err != nil {
		t.Fatalf("deno runtime: %v", err)
	}

	if err := c.WithdrawProjectWorkloads(ctx, testNamespace); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if names := listIngressNames(t, c); len(names) != 1 || names[0] != "db-public" {
		t.Fatalf("ingresses left = %v, want only the one no app owns", names)
	}
	if dep := readDeployment(t, c, app); *dep.Spec.Replicas != 0 || dep.Annotations[appPausedReplicasAnnotation] == "" {
		t.Fatalf("app deployment replicas %d annotations %v, want 0 and the count remembered", *dep.Spec.Replicas, dep.Annotations)
	}
	deno, err := c.clientset.AppsV1().Deployments(testNamespace).Get(ctx, denoRuntimeName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("deno deployment: %v", err)
	}
	if *deno.Spec.Replicas != 0 || deno.Annotations[appPausedReplicasAnnotation] != "1" {
		t.Fatalf("deno replicas %d annotations %v, want stopped with 1 remembered", *deno.Spec.Replicas, deno.Annotations)
	}
	// Idempotent: a repeated withdrawal keeps the remembered counts.
	if err := c.WithdrawProjectWorkloads(ctx, testNamespace); err != nil {
		t.Fatalf("withdraw again: %v", err)
	}
	if dep := readDeployment(t, c, app); dep.Annotations[appPausedReplicasAnnotation] == "0" {
		t.Fatal("a second withdrawal must not overwrite the remembered replica count")
	}
}

func TestWithdrawProjectWorkloads_InAnEmptyNamespaceDoesNothing(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	if err := c.WithdrawProjectWorkloads(context.Background(), testNamespace); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
}

func TestRestartFunctionRuntime_RestoresTheStoppedRuntime(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	if err := c.EnsureDenoRuntime(ctx, testNamespace, DenoRuntimeSpec{Image: "deno:test", RuntimeSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := c.WithdrawProjectWorkloads(ctx, testNamespace); err != nil {
		t.Fatal(err)
	}

	if err := c.RestartFunctionRuntime(ctx, testNamespace); err != nil {
		t.Fatalf("restart: %v", err)
	}
	deno, _ := c.clientset.AppsV1().Deployments(testNamespace).Get(ctx, denoRuntimeName, metav1.GetOptions{})
	if *deno.Spec.Replicas != 1 {
		t.Fatalf("replicas = %d, want 1", *deno.Spec.Replicas)
	}
	if _, kept := deno.Annotations[appPausedReplicasAnnotation]; kept {
		t.Fatal("the remembered count is cleared once restored")
	}
	if err := c.RestartFunctionRuntime(ctx, testNamespace); err != nil {
		t.Fatalf("restart of a running runtime: %v", err)
	}
}

func TestRestartFunctionRuntime_WithoutARuntimeDoesNothing(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	if err := c.RestartFunctionRuntime(context.Background(), testNamespace); err != nil {
		t.Fatalf("restart: %v", err)
	}
}

func TestEnsureDenoRuntime_StartsARuntimeAProjectDeletionStopped(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	spec := DenoRuntimeSpec{Image: "deno:test", RuntimeSecret: "s"}
	if err := c.EnsureDenoRuntime(ctx, testNamespace, spec); err != nil {
		t.Fatal(err)
	}
	if err := c.WithdrawProjectWorkloads(ctx, testNamespace); err != nil {
		t.Fatal(err)
	}

	if err := c.EnsureDenoRuntime(ctx, testNamespace, spec); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	deno, _ := c.clientset.AppsV1().Deployments(testNamespace).Get(ctx, denoRuntimeName, metav1.GetOptions{})
	if *deno.Spec.Replicas != 1 {
		t.Fatalf("replicas = %d, want the runtime running again", *deno.Spec.Replicas)
	}
}

func TestRestoreAppRoute_PutsBackTheIngressAWithdrawalRemoved(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	app := fullApp()
	deployedApp(t, c, app)
	if err := c.WithdrawProjectWorkloads(ctx, testNamespace); err != nil {
		t.Fatal(err)
	}

	if err := c.RestoreAppRoute(ctx, testNamespace, app, testRoute); err != nil {
		t.Fatalf("restore: %v", err)
	}
	ingress, err := c.clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName(app.Name), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ingress: %v", err)
	}
	host, _ := testRoute.Public().Hostname(app.Name, app.ProjectID)
	if ingress.Spec.Rules[0].Host != host {
		t.Fatalf("host = %s, want %s", ingress.Spec.Rules[0].Host, host)
	}
}

func TestRestoreAppRoute_GivesAnInternalServiceNoRoute(t *testing.T) {
	c, _ := newLifecycleFakeClient()
	ctx := context.Background()
	app := fullApp()
	app.Internal = true
	app.InternalPorts = []apphost.InternalPort{{Port: 5432, Protocol: "TCP"}}

	if err := c.RestoreAppRoute(ctx, testNamespace, app, testRoute); err != nil {
		t.Fatalf("restore: %v", err)
	}
	_, err := c.clientset.NetworkingV1().Ingresses(testNamespace).Get(ctx, AppObjectName(app.Name), metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("got %v, an internal service has no ingress", err)
	}
}
