//go:build live

package k8s

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Run with: go test ./internal/k8s/ -tags=integration -run TestK3sAppDeploy -v -timeout 30m
func TestK3sAppDeploy(t *testing.T) {
	for _, platform := range gvisorPlatforms {
		t.Run(platform, func(t *testing.T) { exerciseAppDeploy(t, platform) })
	}
}

func exerciseAppDeploy(t *testing.T, platform string) {
	g := startGVisorK3s(t, platform)
	namespace := g.createNamespace(t, "org1-proj1")
	t.Run("non-root image rolls out", func(t *testing.T) { checkGoodImageRollsOut(t, g, namespace) })
	t.Run("missing image fails fast", func(t *testing.T) { checkMissingImageFailsFast(t, g, namespace) })
	t.Run("crashing image fails only after repeated restarts", func(t *testing.T) { checkCrashLoopTolerance(t, g, namespace) })
	t.Run("fixed image recovers after a failed deploy", func(t *testing.T) { checkRecoveryAfterFailure(t, g, namespace) })
}

func checkGoodImageRollsOut(t *testing.T, g *gvisorCluster, namespace string) {
	if err := g.deployApp(t, namespace, liveApp("good", "nginxinc/nginx-unprivileged:1.27", 8080), 3*time.Minute); err != nil {
		t.Fatalf("want success, got %v", err)
	}
}

func checkMissingImageFailsFast(t *testing.T, g *gvisorCluster, namespace string) {
	start := time.Now()
	err := g.deployApp(t, namespace, liveApp("missing", "docker.io/excalibase/does-not-exist:0", 8080), 3*time.Minute)
	if !errors.Is(err, ErrAppRollout) || !strings.Contains(err.Error(), "Image") {
		t.Fatalf("want an image pull failure, got %v", err)
	}
	if time.Since(start) > 2*time.Minute {
		t.Errorf("took %s, want fail fast", time.Since(start))
	}
	t.Logf("missing image: %v", err)
}

func checkCrashLoopTolerance(t *testing.T, g *gvisorCluster, namespace string) {
	start := time.Now()
	err := g.deployApp(t, namespace, liveApp("crashing", "busybox:1.36", 8080), 4*time.Minute)
	t.Logf("crashing image after %s: %v", time.Since(start).Round(time.Second), err)
	if !errors.Is(err, ErrAppRollout) || !strings.Contains(err.Error(), "CrashLoopBackOff") {
		t.Fatalf("want a crash-loop failure, got %v", err)
	}
	restarts := g.appPod(t, namespace, "crashing").Status.ContainerStatuses[0].RestartCount
	t.Logf("crashing image restarts at failure: %d", restarts)
	if restarts < crashLoopRestartLimit {
		t.Errorf("failed after %d restarts, want at least %d", restarts, crashLoopRestartLimit)
	}
}

func checkRecoveryAfterFailure(t *testing.T, g *gvisorCluster, namespace string) {
	if err := g.deployApp(t, namespace, liveApp("flaky", "docker.io/excalibase/does-not-exist:0", 8080), time.Minute); err == nil {
		t.Fatal("setup: want the bad deploy to fail")
	}
	if err := g.deployApp(t, namespace, liveApp("flaky", "nginxinc/nginx-unprivileged:1.27", 8080), 3*time.Minute); err != nil {
		t.Fatalf("want the fixed deploy to succeed, got %v", err)
	}
}
