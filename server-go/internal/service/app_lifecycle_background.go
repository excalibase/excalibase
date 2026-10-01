package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
)

// EXC-523: a pause, resume or deletion can queue up to three minutes behind
// the app's lease and then wait for its pods, longer than a browser waits for
// one request. The InBackground forms answer with the app as it is and run
// the operation detached from the request; its outcome is the app's status,
// or the LifecycleFailure it records.

// PauseAppInBackground starts PauseApp and answers without waiting for it.
func (s *AppDeployService) PauseAppInBackground(ctx context.Context, projectID, appID string) (*apphost.App, error) {
	return s.lifecycleInBackground(ctx, projectID, appID, nil, func(ctx context.Context) {
		_, _ = s.PauseApp(ctx, projectID, appID)
	})
}

// ResumeAppInBackground starts ResumeApp and answers without waiting for it.
func (s *AppDeployService) ResumeAppInBackground(ctx context.Context, projectID, appID, actor string) (*apphost.App, error) {
	return s.lifecycleInBackground(ctx, projectID, appID, nil, func(ctx context.Context) {
		_, _ = s.ResumeApp(ctx, projectID, appID, actor)
	})
}

// DeleteAppInBackground starts DeleteApp and answers without waiting for it;
// an app with a disk is refused at once unless its erasure is confirmed.
func (s *AppDeployService) DeleteAppInBackground(ctx context.Context, projectID, appID string, confirmDeleteDisk bool) (*apphost.App, error) {
	confirmed := func(app *apphost.App) error {
		if app.Disk != nil && !confirmDeleteDisk {
			return ErrAppDiskDeleteUnconfirmed
		}
		return nil
	}
	return s.lifecycleInBackground(ctx, projectID, appID, confirmed, func(ctx context.Context) {
		_ = s.DeleteApp(ctx, projectID, appID, confirmDeleteDisk)
	})
}

func (s *AppDeployService) lifecycleInBackground(ctx context.Context, projectID, appID string,
	admit func(*apphost.App) error, run func(context.Context)) (*apphost.App, error) {
	app, err := s.lookupApp(projectID, appID)
	if err != nil {
		return nil, err
	}
	if admit != nil {
		if err := admit(app); err != nil {
			return nil, err
		}
	}
	detached := context.WithoutCancel(ctx)
	s.async(func() { run(detached) })
	return app, nil
}

// settleLifecycle records how a pause, resume or deletion ended: a failure in
// words safe to show, or none once one completes. A refusal that changed
// nothing about an app that exists still counts: the caller may not be waiting.
func (s *AppDeployService) settleLifecycle(projectID, appID string, op ProjectOperation, err error) {
	if errors.Is(err, apphost.ErrAppNotFound) || errors.Is(err, ErrAppDiskDeleteUnconfirmed) {
		return
	}
	var failure *apphost.LifecycleFailure
	if err != nil {
		log.Printf("app %s/%s: %s did not complete: %v", projectID, appID, op, err)
		failure = &apphost.LifecycleFailure{Operation: string(op), Reason: lifecycleFailureReason(op, err), At: time.Now().UTC()}
	}
	if recordErr := s.apps.RecordLifecycleFailure(projectID, appID, failure); recordErr != nil &&
		!errors.Is(recordErr, apphost.ErrAppNotFound) {
		log.Printf("app %s/%s: record the %s outcome: %v", projectID, appID, op, recordErr)
	}
}

// namedLifecycleCauses are refusals whose own words are meant for the app's
// developers; they are the ones the API answers with as they are.
var namedLifecycleCauses = []error{
	storage.ErrProjectBusy, apphost.ErrAppStatusConflict, apphost.ErrAppBusy,
	k8s.ErrAppNotDeployed, k8s.ErrAppNotPaused, k8s.ErrAppPodsRemain, k8s.ErrAppRollout, k8s.ErrAppDiskJob,
	ErrAppOverPlan, ErrAppDiskUsageAbovePlan, apphost.ErrDiskAbovePlan, storagebudget.ErrExceeded,
}

// fixedLifecycleCauses are answered in their sentinel's words only.
var fixedLifecycleCauses = []error{ErrOrgTierUnresolved, ErrAppCapacity, ErrAppNoSandboxNode}

func lifecycleFailureReason(op ProjectOperation, err error) string {
	for _, cause := range namedLifecycleCauses {
		if errors.Is(err, cause) {
			return err.Error()
		}
	}
	for _, cause := range fixedLifecycleCauses {
		if errors.Is(err, cause) {
			return cause.Error()
		}
	}
	return fmt.Sprintf("the %s did not complete; try again", op)
}
