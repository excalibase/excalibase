//go:build live

package service

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// projectDeletionStopBudget is how long a deleted project's app may keep answering (EXC-567).
const projectDeletionStopBudget = 10 * time.Second

// Run with: go test ./internal/service/ -tags=live -run TestLiveProjectDeletionTakesTheAppsOffline -v -count=1 -timeout 40m
func TestLiveProjectDeletionTakesTheAppsOffline(t *testing.T) {
	lab := startAppLiveLab(t)
	app := lab.app(t, "proj-gone", "shop", appLiveGoodImage)
	namespace := "org1-" + app.ProjectID
	project := lab.instances.Items[app.ProjectID]
	project.Status, project.NoDatabase = string(domain.StatusActive), true
	svc, deployStore, appStore := lab.service(app)
	first := lab.deploy(t, svc, app)
	lab.expectOutcome(t, deployStore, first.ID, apphost.DeployStatusSucceeded, apphost.StatusRunning)
	lab.expectEdge(t, app, http.StatusOK)
	runtime := k8s.DenoRuntimeSpec{Image: appLiveGoodImage, RuntimeSecret: "live"}
	if err := lab.client.EnsureDenoRuntime(lab.ctx, namespace, runtime); err != nil {
		t.Fatalf("function runtime: %v", err)
	}

	prov := NewProvisioningService(lab.instances, nil, lab.client)
	prov.SetSelfHostedMode(true)
	prov.SetProjectWorkloadStopper(svc)

	t.Run("the app stops answering within seconds of the delete", func(t *testing.T) {
		started := time.Now()
		done := make(chan error, 1)
		go func() {
			_, err := prov.ScheduleDeletion(lab.ctx, app.ProjectID, DeprovisionOptions{})
			done <- err
		}()
		offline := lab.waitUntilEdgeStops(t, app, projectDeletionStopBudget)
		t.Logf("the app stopped answering %s after the delete", offline.Sub(started).Round(100*time.Millisecond))
		if err := <-done; err != nil {
			t.Fatalf("ScheduleDeletion: %v", err)
		}
		if got := lab.instances.Items[app.ProjectID].Status; got != string(domain.StatusPendingDeletion) {
			t.Fatalf("project status = %s, want PENDING_DELETION", got)
		}
		if pods := lab.readyPods(t, app); pods != "" {
			t.Fatalf("app pods still running: %s", pods)
		}
		if got := appStore.statusOf(app.ProjectID, app.ID); got != apphost.StatusStopped {
			t.Fatalf("app status = %s, want %s", got, apphost.StatusStopped)
		}
		lab.expectRuntimeReplicas(t, namespace, 0)
	})

	t.Run("a cancelled deletion brings the runtime back and a resume serves the app again", func(t *testing.T) {
		if err := prov.CancelDeletion(lab.ctx, app.ProjectID); err != nil {
			t.Fatalf("CancelDeletion: %v", err)
		}
		lab.expectRuntimeReplicas(t, namespace, 1)
		if _, err := svc.ResumeApp(lab.ctx, app.ProjectID, app.ID, "live"); err != nil {
			t.Fatalf("ResumeApp: %v", err)
		}
		lab.expectEdge(t, app, http.StatusOK)
	})
}

// waitUntilEdgeStops polls the app's host through the edge until it no longer answers 200.
func (lab *appLiveLab) waitUntilEdgeStops(t *testing.T, app *apphost.App, budget time.Duration) time.Time {
	t.Helper()
	host, err := appLiveRoute.Public().Hostname(app.Name, app.ProjectID)
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	url := "http://" + net.JoinHostPort(lab.nodeIP, strconv.Itoa(appLiveNodePort)) + "/"
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequest(http.MethodGet, url, nil)
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			return time.Now()
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Logf("edge %s now answers %d", host, response.StatusCode)
			return time.Now()
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("the app still answered 200 at %s, %s after the delete", host, budget)
	return time.Time{}
}

func (lab *appLiveLab) expectRuntimeReplicas(t *testing.T, namespace string, want int32) {
	t.Helper()
	dep, err := lab.cs.AppsV1().Deployments(namespace).Get(lab.ctx, "deno-runtime", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("function runtime: %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != want {
		t.Fatalf("function runtime replicas = %v, want %d", dep.Spec.Replicas, want)
	}
}
