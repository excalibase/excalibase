package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// A queued deploy is recorded pending and touches nothing until it runs.
func TestQueueDeploy_RecordsThenRuns(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	queued, run, err := svc.QueueDeploy(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("QueueDeploy: %v", err)
	}
	if queued.Status != apphost.DeployStatusPending || deploys.getByID(queued.ID) == nil {
		t.Fatalf("queued %+v", queued)
	}
	if countCalls(kube, "CreateAppDisk") != 0 || countCalls(kube, "ApplyAppWorkload") != 0 {
		t.Fatalf("queueing touched the cluster: %v", kube.Calls)
	}
	run()
	waitForDeployStatus(t, deploys, queued.ID, apphost.DeployStatusSucceeded)
	if countCalls(kube, "ApplyAppWorkload") != 1 {
		t.Fatalf("the run applies the workload once: %v", kube.Calls)
	}
}

// A queued deploy a newer one superseded never applies its older config.
func TestQueueDeploy_ASupersededDeployDoesNotRun(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	queued, run, err := svc.QueueDeploy(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("QueueDeploy: %v", err)
	}
	newer, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	run()
	if countCalls(kube, "ApplyAppWorkload") != 1 {
		t.Fatalf("only the newer deploy applies: %v", kube.Calls)
	}
	if got := deploys.getByID(queued.ID); got.Status != apphost.DeployStatusSuperseded {
		t.Fatalf("queued deploy %s", got.Status)
	}
	waitForDeployStatus(t, deploys, newer.ID, apphost.DeployStatusSucceeded)
}

// A run that cannot start ends its deploy failed rather than pending.
func TestQueueDeploy_ARunThatCannotStartFailsTheDeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	queued, run, err := svc.QueueDeploy(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("QueueDeploy: %v", err)
	}
	svc.apps.(*fakeAppStoreForDeploy).getErr = errors.New("store unavailable")
	run()
	failed := waitForDeployStatus(t, deploys, queued.ID, apphost.DeployStatusFailed)
	if failed.FailureReason == "" || strings.Contains(failed.FailureReason, "store unavailable") {
		t.Fatalf("a failed deploy says why in its own words: %q", failed.FailureReason)
	}
}
