//go:build live

package k8s

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sAppLifecycle -v -timeout 30m
func TestK3sAppLifecycle(t *testing.T) {
	g := startGVisorK3s(t, gvisorPlatformSystrap)
	namespace := g.createNamespace(t, "org1-proj-life")
	app := liveApp("life", "nginxinc/nginx-unprivileged:1.27", 8080)
	app.Replicas = 1
	if err := g.deployApp(t, namespace, app, 3*time.Minute); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	ctx := context.Background()

	if err := g.client.PauseAppWorkload(ctx, namespace, app.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	start := time.Now()
	if err := g.client.WaitForAppPodsGone(ctx, namespace, app.ID, 2*time.Minute); err != nil {
		t.Fatalf("pods after pause: %v", err)
	}
	t.Logf("pods gone %s after pause", time.Since(start).Round(time.Second))
	g.assertPodCount(t, namespace, app.ID, 0)

	if err := g.client.ResumeAppWorkload(ctx, namespace, app.ID, app.Name, 3*time.Minute); err != nil {
		t.Fatalf("resume: %v", err)
	}
	g.assertPodCount(t, namespace, app.ID, 1)

	start = time.Now()
	if err := g.client.DeleteAppWorkload(ctx, namespace, app.ID, 2*time.Minute); err != nil {
		t.Fatalf("delete: %v", err)
	}
	t.Logf("workload gone %s after delete", time.Since(start).Round(time.Second))
	g.assertNothingOwned(t, namespace, app.ID)
}

func (g *gvisorCluster) assertPodCount(t *testing.T, namespace, appID string, want int) {
	t.Helper()
	pods, err := g.clientset.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: appOwnedSelector(appID)})
	if err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != want {
		t.Fatalf("%d pods, want %d", len(pods.Items), want)
	}
}

func (g *gvisorCluster) assertNothingOwned(t *testing.T, namespace, appID string) {
	t.Helper()
	ctx := context.Background()
	opts := metav1.ListOptions{LabelSelector: appOwnedSelector(appID)}
	counts := map[string]func() (int, error){
		"pods": func() (int, error) {
			l, err := g.clientset.CoreV1().Pods(namespace).List(ctx, opts)
			return lenOr(err, func() int { return len(l.Items) })
		},
		"replicasets": func() (int, error) {
			l, err := g.clientset.AppsV1().ReplicaSets(namespace).List(ctx, opts)
			return lenOr(err, func() int { return len(l.Items) })
		},
		"deployments": func() (int, error) {
			l, err := g.clientset.AppsV1().Deployments(namespace).List(ctx, opts)
			return lenOr(err, func() int { return len(l.Items) })
		},
		"services": func() (int, error) {
			l, err := g.clientset.CoreV1().Services(namespace).List(ctx, opts)
			return lenOr(err, func() int { return len(l.Items) })
		},
		"secrets": func() (int, error) {
			l, err := g.clientset.CoreV1().Secrets(namespace).List(ctx, opts)
			return lenOr(err, func() int { return len(l.Items) })
		},
		"ingresses": func() (int, error) {
			l, err := g.clientset.NetworkingV1().Ingresses(namespace).List(ctx, opts)
			return lenOr(err, func() int { return len(l.Items) })
		},
		"network policies": func() (int, error) {
			l, err := g.client.dynamicClient.Resource(CiliumNetworkPolicyGVR).Namespace(namespace).List(ctx, opts)
			return lenOr(err, func() int { return len(l.Items) })
		},
	}
	for kind, count := range counts {
		n, err := count()
		if err != nil {
			t.Errorf("list %s: %v", kind, err)
			continue
		}
		if n != 0 {
			t.Errorf("%d %s left behind", n, kind)
		}
	}
}

func lenOr(err error, n func() int) (int, error) {
	if err != nil {
		return 0, err
	}
	return n(), nil
}
