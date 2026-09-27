//go:build live

package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// Run with: go test ./internal/k8s/ -tags=live -run TestK3sAppLogs -v -timeout 30m
func TestK3sAppLogs(t *testing.T) {
	g := startGVisorK3s(t, gvisorPlatformSystrap)
	namespace := g.createNamespace(t, "org1-proj-logs")
	web := liveApp("web", "nginxinc/nginx-unprivileged:1.27", 8080)
	other := liveApp("other", "nginxinc/nginx-unprivileged:1.27", 8080)
	for _, app := range []*apphost.App{web, other} {
		if err := g.deployApp(t, namespace, app, 3*time.Minute); err != nil {
			t.Fatalf("deploy %s: %v", app.Name, err)
		}
	}
	ctx := context.Background()

	var lines []AppLogLine
	eventually(t, "the app has written log lines", time.Minute, func() bool {
		got, err := g.client.AppLogs(ctx, namespace, web.ID, AppLogOptions{TailLines: 100})
		if err != nil {
			t.Fatalf("AppLogs: %v", err)
		}
		lines = got.Lines
		return len(got.Lines) > 0
	})
	webPod := g.appPod(t, namespace, "web").Name
	for _, line := range lines {
		if line.Pod != webPod {
			t.Fatalf("a line from %s, which is not the app's pod %s", line.Pod, webPod)
		}
		if line.Time.IsZero() {
			t.Fatalf("a line without its time: %+v", line)
		}
	}
	t.Logf("%d lines, last: %s", len(lines), lines[len(lines)-1].Text)

	since := lines[len(lines)-1].Time
	newer, err := g.client.AppLogs(ctx, namespace, web.ID, AppLogOptions{TailLines: 100, Since: &since})
	if err != nil {
		t.Fatalf("AppLogs since: %v", err)
	}
	for _, line := range newer.Lines {
		if !line.Time.After(since) {
			t.Fatalf("a line the cursor already covered came back: %+v", line)
		}
	}
}
