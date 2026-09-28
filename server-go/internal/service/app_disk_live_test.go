//go:build live

package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

// Run with: go test ./internal/service/ -tags=live -run TestLiveAppDisk -v -count=1 -timeout 40m
// The sandbox runtime under a disk is proven by k8s TestK3sAppDiskUnderGVisor; this drives the deploy service.
func TestLiveAppDisk(t *testing.T) {
	lab := startAppLiveLab(t)
	app := lab.app(t, "proj-disk", "keeper", appLiveGoodImage)
	namespace := "org1-" + app.ProjectID
	svc, deploys, apps := lab.service(app)
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	svc.SetDiskLimits(NewAppDiskLimits(NewOrgPlanTiers(lab.instances, orgs),
		fixedTierConfigs{domain.Free: config.TierConfig{MaxAppDiskSize: "1Gi"}}))
	svc.render.DiskStorageClass = "local-path"
	setDisk := func(disk *apphost.AppDisk) {
		apps.mu.Lock()
		apps.apps[app.ProjectID+"/"+app.ID].Disk = disk
		apps.mu.Unlock()
	}

	t.Run("a disk above the plan is refused and nothing is made", func(t *testing.T) {
		setDisk(&apphost.AppDisk{MountPath: "/data", Size: "2Gi"})
		deploy := lab.deploy(t, svc, app)
		if deploy.Status != apphost.DeployStatusFailed || !strings.Contains(deploy.FailureReason, "plan allows up to 1Gi") {
			t.Fatalf("got %q %q, want a refusal naming the 1Gi plan cap", deploy.Status, deploy.FailureReason)
		}
		lab.expectClaims(t, namespace, 0)
		t.Logf("refused: %s", deploy.FailureReason)
	})

	setDisk(&apphost.AppDisk{MountPath: "/data", Size: "1Gi"})
	first := lab.deploy(t, svc, app)
	lab.expectOutcome(t, deploys, first.ID, apphost.DeployStatusSucceeded, apphost.StatusRunning)
	lab.expectClaims(t, namespace, 1)
	lab.execInApp(t, namespace, app, "echo written-before-redeploy > /data/proof.txt && sync")

	t.Run("a redeploy keeps the file", func(t *testing.T) {
		redeploy := lab.deploy(t, svc, app)
		lab.expectOutcome(t, deploys, redeploy.ID, apphost.DeployStatusSucceeded, apphost.StatusRunning)
		lab.expectProof(t, namespace, app)
	})
	t.Run("a rename keeps the file", func(t *testing.T) {
		apps.mu.Lock()
		apps.apps[app.ProjectID+"/"+app.ID].Name = "keeper-two"
		apps.mu.Unlock()
		app.Name = "keeper-two"
		renamed := lab.deploy(t, svc, app)
		lab.expectOutcome(t, deploys, renamed.ID, apphost.DeployStatusSucceeded, apphost.StatusRunning)
		lab.expectProof(t, namespace, app)
		lab.expectClaims(t, namespace, 1)
	})
	t.Run("a pause and a resume keep the file", func(t *testing.T) {
		// The fake deploy store records the app status a deploy observed; the real store writes it to the app row.
		apps.mu.Lock()
		apps.apps[app.ProjectID+"/"+app.ID].Status = deploys.appStatusOf(app.ID)
		apps.mu.Unlock()
		if _, err := svc.PauseApp(lab.ctx, app.ProjectID, app.ID); err != nil {
			t.Fatalf("pause: %v", err)
		}
		lab.expectClaims(t, namespace, 1)
		if _, err := svc.ResumeApp(lab.ctx, app.ProjectID, app.ID, "live"); err != nil {
			t.Fatalf("resume: %v", err)
		}
		lab.expectProof(t, namespace, app)
	})
	t.Run("growing past the plan and growing on local-path are refused", func(t *testing.T) {
		if _, err := svc.GrowAppDisk(lab.ctx, app.ProjectID, app.ID, "2Gi"); !errors.Is(err, apphost.ErrDiskAbovePlan) {
			t.Fatalf("above the plan: got %v, want ErrDiskAbovePlan", err)
		}
		svc.SetDiskLimits(NewAppDiskLimits(NewOrgPlanTiers(lab.instances, orgs),
			fixedTierConfigs{domain.Free: config.TierConfig{MaxAppDiskSize: "5Gi"}}))
		_, err := svc.GrowAppDisk(lab.ctx, app.ProjectID, app.ID, "2Gi")
		if !errors.Is(err, k8s.ErrAppDiskNotExpandable) {
			t.Fatalf("on local-path: got %v, want ErrAppDiskNotExpandable", err)
		}
		t.Logf("refused: %v", err)
	})
	t.Run("deleting needs the confirmation, then removes the disk", func(t *testing.T) {
		if err := svc.DeleteApp(lab.ctx, app.ProjectID, app.ID, false); !errors.Is(err, ErrAppDiskDeleteUnconfirmed) {
			t.Fatalf("unconfirmed: got %v", err)
		}
		lab.expectProof(t, namespace, app)
		if err := svc.DeleteApp(lab.ctx, app.ProjectID, app.ID, true); err != nil {
			t.Fatalf("confirmed: %v", err)
		}
		lab.expectClaims(t, namespace, 0)
	})
}

func (lab *appLiveLab) expectClaims(t *testing.T, namespace string, want int) {
	t.Helper()
	claims, err := lab.cs.CoreV1().PersistentVolumeClaims(namespace).List(lab.ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	if len(claims.Items) != want {
		t.Fatalf("%d claims in %s, want %d", len(claims.Items), namespace, want)
	}
}

func (lab *appLiveLab) servingPod(t *testing.T, namespace string, app *apphost.App) corev1.Pod {
	t.Helper()
	var serving corev1.Pod
	eventuallyLive(t, "one ready pod of "+app.Name, 3*time.Minute, func() bool {
		pods, err := lab.cs.CoreV1().Pods(namespace).List(lab.ctx, metav1.ListOptions{LabelSelector: "excalibase.io/app=" + app.ID})
		if err != nil || len(pods.Items) != 1 {
			return false
		}
		serving = pods.Items[0]
		for _, condition := range serving.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	})
	return serving
}

func (lab *appLiveLab) execInApp(t *testing.T, namespace string, app *apphost.App, script string) string {
	t.Helper()
	pod := lab.servingPod(t, namespace, app)
	out, err := lab.client.ExecInPod(lab.ctx, namespace, pod.Name, pod.Spec.Containers[0].Name, []string{"sh", "-c", script})
	if err != nil {
		t.Fatalf("exec in %s: %v", pod.Name, err)
	}
	return out
}

func (lab *appLiveLab) expectProof(t *testing.T, namespace string, app *apphost.App) {
	t.Helper()
	got := strings.TrimSpace(lab.execInApp(t, namespace, app, "cat /data/proof.txt"))
	if got != "written-before-redeploy" {
		t.Fatalf("/data/proof.txt = %q", got)
	}
	t.Logf("pod of %s reads %q from its disk", app.Name, got)
}
