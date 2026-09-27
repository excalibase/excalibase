package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const wantRolloutBudget = 10 * time.Minute

// rolloutTimesOut answers the way the cluster client does when the budget runs
// out, and records the budget it was given.
func rolloutTimesOut(budget *time.Duration) func(context.Context, string, string, string, time.Duration) error {
	return func(_ context.Context, _, name, _ string, timeout time.Duration) error {
		*budget = timeout
		return fmt.Errorf("%w: %s did not become ready within %s", k8s.ErrAppRollout, name, timeout)
	}
}

func TestNewAppDeployService_RolloutTimeoutIsTenMinutes(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	if svc.timeout != wantRolloutBudget {
		t.Fatalf("rollout timeout: got %s want %s", svc.timeout, wantRolloutBudget)
	}
}

func TestDeployApp_ARolloutPastTheTimeoutFailsWithTheReason(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	var budget time.Duration
	kube.AppRolloutFunc = rolloutTimesOut(&budget)

	returned, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if budget != wantRolloutBudget {
		t.Errorf("rollout watched for %s, want %s", budget, wantRolloutBudget)
	}
	failed := waitForDeployStatus(t, deploys, returned.ID, apphost.DeployStatusFailed)
	if !strings.Contains(failed.FailureReason, "did not become ready within 10m0s") {
		t.Errorf("failure reason must name the timeout: %q", failed.FailureReason)
	}
	if failed.FinishedAt == nil {
		t.Error("a timed-out deploy must record when it finished")
	}
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusFailed {
		t.Errorf("app status: got %q want %q", got, apphost.StatusFailed)
	}
	svc.mu.Lock()
	_, stillWatched := svc.active[app.ID]
	svc.mu.Unlock()
	if stillWatched {
		t.Error("a timed-out rollout must stop being watched")
	}
}

func TestDeployApp_ARedeployPastTheTimeoutKeepsTheServingAppActive(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	var budget time.Duration
	kube.AppRolloutFunc = rolloutTimesOut(&budget)
	kube.AppAvailable[rolloutKey(app, testDeployNamespace)] = 1

	returned, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	waitForDeployStatus(t, deploys, returned.ID, apphost.DeployStatusFailed)
	if got := deploys.appStatusOf(app.ID); got != apphost.StatusRunning {
		t.Fatalf("app status: got %q want %q", got, apphost.StatusRunning)
	}
}

func TestResumeRollouts_AResumedWatchGetsTheFullTimeout(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	orphan := seedUnfinishedDeploy(t, deploys, app, apphost.DeployStatusRolling)
	var budget time.Duration
	kube.AppRolloutFunc = rolloutTimesOut(&budget)

	if err := svc.ResumeRollouts(context.Background()); err != nil {
		t.Fatalf("ResumeRollouts: %v", err)
	}
	if budget != wantRolloutBudget {
		t.Errorf("resumed watch ran for %s, want %s", budget, wantRolloutBudget)
	}
	failed := waitForDeployStatus(t, deploys, orphan.ID, apphost.DeployStatusFailed)
	if !strings.Contains(failed.FailureReason, "within 10m0s") {
		t.Errorf("failure reason must name the timeout: %q", failed.FailureReason)
	}
}
