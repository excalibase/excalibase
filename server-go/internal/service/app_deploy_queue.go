package service

import (
	"context"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// deployNotStarted is what a queued deploy that could not start says; the cause goes to the log.
const deployNotStarted = "the deploy could not start; deploy the app again"

// QueueDeploy records the app's current config as a pending deploy and
// returns it with run, which applies it (EXC-669): a template answers once
// every app has its record, and a new disk is prepared after the answer. A
// deploy never run stays pending, and the rollout sweeper ends it failed.
func (s *AppDeployService) QueueDeploy(ctx context.Context, projectID, appID, actor string) (*apphost.Deploy, func(), error) {
	ctx = context.WithoutCancel(ctx)
	release, err := s.holdAppForDeploy(ctx, projectID, appID)
	if err != nil {
		return nil, nil, err
	}
	app, err := s.lookupApp(projectID, appID)
	if err != nil {
		release()
		return nil, nil, err
	}
	planned, err := s.record(ctx, app, apphost.ConfigFromApp(app), deployMeta{origin: apphost.DeployOrigin{Actor: actor}})
	release()
	if err != nil {
		return nil, nil, err
	}
	// The caller reads its own copy; run writes the planned one.
	queued := *planned.deploy
	return &queued, func() { s.runQueued(ctx, planned) }, nil
}

// runQueued applies a queued deploy unless a newer deploy superseded it meanwhile.
func (s *AppDeployService) runQueued(ctx context.Context, planned *plannedDeploy) {
	deploy := planned.deploy
	_, err := s.underLease(ctx, deploy.ProjectID, deploy.AppID, func(app *apphost.App) (*apphost.Deploy, func(), error) {
		current, err := s.deploys.Get(deploy.ProjectID, deploy.AppID, deploy.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("look up deploy: %w", err)
		}
		if current == nil || current.Status != apphost.DeployStatusPending {
			return current, nil, nil
		}
		return s.apply(ctx, app, planned)
	})
	if err != nil {
		log.Printf("deploy %s of app %s/%s did not start: %v", deploy.ID, deploy.ProjectID, deploy.AppID, err)
		s.finish(deploy, apphost.DeployStatusFailed, deployNotStarted, "")
	}
}
