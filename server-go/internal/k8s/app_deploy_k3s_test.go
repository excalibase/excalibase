//go:build integration

package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Run with: go test ./internal/k8s/ -tags=integration -run TestK3sAppDeploy -v -timeout 15m
func TestK3sAppDeploy(t *testing.T) {
	ctx := context.Background()
	container, err := k3s.Run(ctx, "rancher/k3s:v1.31.6-k3s1")
	if err != nil {
		t.Fatalf("k3s start: %v", err)
	}
	defer container.Terminate(ctx)
	kubeconfig, err := container.GetKubeConfig(ctx)
	if err != nil {
		t.Fatalf("get kubeconfig: %v", err)
	}
	restCfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("clientset: %v", err)
	}
	client := &Client{clientset: cs, restConfig: restCfg}

	const namespace = "org1-proj1"
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	deploy := func(name, image string, timeout time.Duration) error {
		app := &apphost.App{ID: "app-" + name, ProjectID: "proj1", Name: name, Image: image,
			Port: 8080, Replicas: 1, Tier: domain.Free, Status: apphost.StatusCreated}
		workload, err := RenderAppWorkload(namespace, app, nil)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		if err := client.ApplyAppWorkload(ctx, namespace, workload); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		return client.WaitForAppRollout(ctx, namespace, AppObjectName(name), timeout)
	}

	t.Run("non-root image rolls out", func(t *testing.T) {
		if err := deploy("good", "registry.k8s.io/pause:3.10", 3*time.Minute); err != nil {
			t.Fatalf("want success, got %v", err)
		}
	})

	t.Run("missing image fails fast", func(t *testing.T) {
		start := time.Now()
		err := deploy("missing", "docker.io/excalibase/does-not-exist:0", 3*time.Minute)
		if !errors.Is(err, ErrAppRollout) || !strings.Contains(err.Error(), "Image") {
			t.Fatalf("want an image pull failure, got %v", err)
		}
		if time.Since(start) > 2*time.Minute {
			t.Errorf("took %s, want fail fast", time.Since(start))
		}
		t.Logf("missing image: %v", err)
	})

	t.Run("root image fails fast", func(t *testing.T) {
		start := time.Now()
		err := deploy("rooted", "busybox:1.36", 3*time.Minute)
		t.Logf("root image after %s: %v", time.Since(start).Round(time.Second), err)
		if !errors.Is(err, ErrAppRollout) || !strings.Contains(err.Error(), "CreateContainerConfigError") {
			t.Fatalf("want a refused root image, got %v", err)
		}
		if time.Since(start) > 2*time.Minute {
			t.Errorf("took %s, want fail fast", time.Since(start))
		}
	})

	t.Run("fixed image recovers after a failed deploy", func(t *testing.T) {
		if err := deploy("flaky", "docker.io/excalibase/does-not-exist:0", time.Minute); err == nil {
			t.Fatal("setup: want the bad deploy to fail")
		}
		if err := deploy("flaky", "registry.k8s.io/pause:3.10", 3*time.Minute); err != nil {
			t.Fatalf("want the fixed deploy to succeed, got %v", err)
		}
	})
}
