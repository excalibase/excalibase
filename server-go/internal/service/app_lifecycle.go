package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// defaultAppStopTimeout covers the drain and the grace period of every pod of an app.
const defaultAppStopTimeout = 3 * time.Minute

const (
	defaultDeployLeaseWait = 30 * time.Second
	deployLeaseRetry       = 200 * time.Millisecond
	// defaultLifecycleLeaseWait outlasts a disk measurement, whose probe Job
	// is bounded at two minutes.
	defaultLifecycleLeaseWait = 3 * time.Minute
)

// appLeaseKey names the project too, so a caller naming another project's app id
// cannot take that app's lease; project and app ids hold neither ':' nor '/'.
func appLeaseKey(projectID, appID string) string { return "app:" + projectID + "/" + appID }

// AppSecretPurger removes every secret value stored under a prefix.
type AppSecretPurger interface {
	DeletePrefix(prefix string) (int, error)
}

// SetOperationClaimer shares the lifecycle lease across replicas; deploys,
// pauses, resumes and deletions of one app then exclude one another everywhere.
func (s *AppDeployService) SetOperationClaimer(claimer ProjectOperationClaimer) {
	s.claimer = claimer
}

func (s *AppDeployService) SetSecretPurger(purger AppSecretPurger) { s.secrets = purger }

func (s *AppDeployService) holdApp(ctx context.Context, projectID, appID string, op ProjectOperation) (func(), error) {
	release, claimed, err := s.claimer.Claim(ctx, appLeaseKey(projectID, appID), op)
	if err != nil {
		return nil, fmt.Errorf("claim app for %s: %w", op, err)
	}
	if !claimed {
		return nil, ErrProjectOperationRunning
	}
	return release, nil
}

// holdAppForDeploy waits briefly for the lease, so a deploy made while an
// earlier one is still being applied is queued behind it rather than refused.
func (s *AppDeployService) holdAppForDeploy(ctx context.Context, projectID, appID string) (func(), error) {
	return s.holdAppQueued(ctx, projectID, appID, OperationDeploy, s.deployLeaseWait)
}

// holdAppForLifecycle queues a pause, resume or deletion behind whatever holds
// the app, so a short read such as a disk measurement does not refuse it.
func (s *AppDeployService) holdAppForLifecycle(ctx context.Context, projectID, appID string, op ProjectOperation) (func(), error) {
	return s.holdAppQueued(ctx, projectID, appID, op, s.lifecycleLeaseWait)
}

func (s *AppDeployService) holdAppQueued(ctx context.Context, projectID, appID string, op ProjectOperation, wait time.Duration) (func(), error) {
	deadline := time.Now().Add(wait)
	for {
		release, err := s.holdApp(ctx, projectID, appID, op)
		if !errors.Is(err, ErrProjectOperationRunning) || time.Now().After(deadline) {
			return release, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(deployLeaseRetry):
		}
	}
}

// PauseApp scales the app to zero and records PAUSED only once no pod of it is
// left. A pause that stops part way stays PAUSING and is finished by pausing again.
func (s *AppDeployService) PauseApp(ctx context.Context, projectID, appID string) (paused *apphost.App, err error) {
	defer func() { s.settleLifecycle(projectID, appID, OperationPause, err) }()
	ctx = context.WithoutCancel(ctx)
	release, err := s.holdAppForLifecycle(ctx, projectID, appID, OperationPause)
	if err != nil {
		return nil, err
	}
	defer release()

	app, err := s.lookupApp(projectID, appID)
	if err != nil || app.Status == apphost.StatusStopped {
		return app, err
	}
	namespace, err := s.namespaceFor(projectID)
	if err != nil {
		return nil, s.noWorkload(err)
	}
	if _, err := s.apps.Transition(projectID, appID,
		[]string{apphost.StatusRunning, apphost.StatusFailed, apphost.StatusPausing}, apphost.StatusPausing); err != nil {
		return nil, err
	}
	s.cancelActive(appID)
	if err := s.kube.PauseAppWorkload(ctx, namespace, appID); err != nil {
		return nil, s.putBack(projectID, appID, apphost.StatusPausing, app.Status, err, k8s.ErrAppNotDeployed)
	}
	if err := s.kube.WaitForAppPodsGone(ctx, namespace, appID, s.stopTimeout); err != nil {
		return nil, err
	}
	return s.apps.Transition(projectID, appID, []string{apphost.StatusPausing}, apphost.StatusStopped)
}

// ResumeApp restores what the pause stopped and records ACTIVE once it is
// ready. A rollout that fails leaves the app FAILED; any other failure leaves
// it RESUMING, for a retry.
func (s *AppDeployService) ResumeApp(ctx context.Context, projectID, appID, actor string) (resumed *apphost.App, err error) {
	defer func() { s.settleLifecycle(projectID, appID, OperationResume, err) }()
	ctx = context.WithoutCancel(ctx)
	release, err := s.holdAppForLifecycle(ctx, projectID, appID, OperationResume)
	if err != nil {
		return nil, err
	}
	defer release()

	app, err := s.lookupApp(projectID, appID)
	if err != nil || app.Status == apphost.StatusRunning {
		return app, err
	}
	namespace, err := s.namespaceFor(projectID)
	if err != nil {
		return nil, s.noWorkload(err)
	}
	if _, err := s.apps.Transition(projectID, appID,
		[]string{apphost.StatusStopped, apphost.StatusFailed, apphost.StatusResuming}, apphost.StatusResuming); err != nil {
		return nil, err
	}
	size, err := s.admitResume(ctx, namespace, app)
	if err != nil {
		return nil, s.putBack(projectID, appID, apphost.StatusResuming, app.Status, err, resumeRefusals...)
	}
	// A disk above the plan is lowered before the app comes back, or the resume is refused.
	fitted, err := s.fitDiskToPlan(ctx, namespace, app)
	if err != nil {
		return nil, s.putBack(projectID, appID, apphost.StatusResuming, app.Status, err, diskRefusals...)
	}
	app = fitted
	if app.Disk != nil {
		if err := s.kube.RepointAppDisk(ctx, namespace, appID, k8s.AppDiskClaimName(appID, app.Disk.Generation)); err != nil {
			return nil, err
		}
	}
	if err := s.recordResize(app, size, actor); err != nil {
		return nil, err
	}
	err = s.kube.ResumeAppWorkload(ctx, namespace, appID, app.Name, size.tier, s.timeout)
	switch {
	case err == nil:
		if err := s.restoreRoutes(ctx, namespace, app); err != nil {
			return nil, err
		}
		return s.apps.Transition(projectID, appID, []string{apphost.StatusResuming}, apphost.StatusRunning)
	case errors.Is(err, k8s.ErrAppRollout):
		if _, failErr := s.apps.Transition(projectID, appID, []string{apphost.StatusResuming}, apphost.StatusFailed); failErr != nil {
			return nil, errors.Join(err, failErr)
		}
		return nil, err
	default:
		return nil, s.putBack(projectID, appID, apphost.StatusResuming, app.Status, err, k8s.ErrAppNotPaused, k8s.ErrAppNotDeployed)
	}
}

// DeleteApp tears the workload down, waits until none of its pods is left,
// removes its disk and secret values and only then forgets the app. An app
// with a disk is deleted only when confirmDeleteDisk says its data may go.
// Anything that fails leaves it DELETING, and deleting again carries on from there.
func (s *AppDeployService) DeleteApp(ctx context.Context, projectID, appID string, confirmDeleteDisk bool) (err error) {
	defer func() { s.settleLifecycle(projectID, appID, OperationDeletion, err) }()
	ctx = context.WithoutCancel(ctx)
	release, err := s.holdAppForLifecycle(ctx, projectID, appID, OperationDeletion)
	if err != nil {
		return err
	}
	defer release()

	app, err := s.lookupApp(projectID, appID)
	if err != nil {
		return err
	}
	if app.Disk != nil && !confirmDeleteDisk {
		return ErrAppDiskDeleteUnconfirmed
	}
	if _, err := s.apps.Transition(projectID, appID, apphost.AllStatuses(), apphost.StatusDeleting); err != nil {
		return err
	}
	s.cancelActive(appID)
	if err := s.deleteWorkload(ctx, projectID, appID); err != nil {
		return err
	}
	if s.secrets != nil {
		if _, err := s.secrets.DeletePrefix(apphost.AppSecretPrefix(projectID, appID)); err != nil {
			return fmt.Errorf("delete the app's secret values: %w", err)
		}
	}
	if s.corsOrigins != nil {
		if _, err := s.corsOrigins.ReleaseAppCorsOrigins(ctx, projectID, appID); err != nil {
			return fmt.Errorf("remove the app's origin from the CORS allowlist: %w", err)
		}
	}
	return s.apps.Delete(projectID, appID)
}

// deleteWorkload treats a project with no namespace as holding no workload: nothing was ever deployed into it.
func (s *AppDeployService) deleteWorkload(ctx context.Context, projectID, appID string) error {
	namespace, err := s.namespaceFor(projectID)
	if errors.Is(err, errNoAppNamespace) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.kube.DeleteAppWorkload(ctx, namespace, appID, s.stopTimeout)
}

func (s *AppDeployService) lookupApp(projectID, appID string) (*apphost.App, error) {
	app, err := s.apps.Get(projectID, appID)
	if err != nil {
		return nil, fmt.Errorf("look up app: %w", err)
	}
	if app == nil {
		return nil, apphost.ErrAppNotFound
	}
	return app, nil
}

func (s *AppDeployService) noWorkload(err error) error {
	if errors.Is(err, errNoAppNamespace) {
		return k8s.ErrAppNotDeployed
	}
	return err
}

// putBack returns the app to the status it had when the cluster showed there
// was nothing to do; any other failure keeps the in-progress status for a retry.
func (s *AppDeployService) putBack(projectID, appID, current, previous string, cause error, nothingToDo ...error) error {
	for _, target := range nothingToDo {
		if errors.Is(cause, target) {
			if _, err := s.apps.Transition(projectID, appID, []string{current}, previous); err != nil {
				return errors.Join(cause, err)
			}
			return cause
		}
	}
	return cause
}
