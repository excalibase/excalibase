package service

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// StopProjectWorkloads is what a project deletion does to what the project
// serves besides its database (EXC-567): every app route and custom domain
// is withdrawn at once, then each app that was serving is paused and waited
// on until none of its pods is left. The function runtime is stopped with
// them. Disks, variables and domain records stay for a cancelled deletion.
func (s *AppDeployService) StopProjectWorkloads(ctx context.Context, projectID string) error {
	namespace, err := s.namespaceFor(projectID)
	if errors.Is(err, errNoAppNamespace) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.kube.WithdrawProjectWorkloads(ctx, namespace); err != nil {
		return fmt.Errorf("withdraw the project's apps: %w", err)
	}
	apps, err := s.apps.List(projectID)
	if err != nil {
		return fmt.Errorf("list the project's apps: %w", err)
	}
	return s.pauseServing(ctx, projectID, namespace, apps)
}

// pauseServing pauses every app, or waits for the pods of one PauseApp does
// not take, all at once so a project with many apps stops within one drain period.
func (s *AppDeployService) pauseServing(ctx context.Context, projectID, namespace string, apps []*apphost.App) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, app := range apps {
		wg.Add(1)
		go func(appID, status string) {
			defer wg.Done()
			var err error
			if mayHavePods(status) {
				_, err = s.PauseApp(ctx, projectID, appID)
			} else {
				// Not ACTIVE yet (a first rollout) or already stopped: the
				// withdrawal scaled it to zero; wait for any pods it still has.
				err = s.kube.WaitForAppPodsGone(ctx, namespace, appID, s.stopTimeout)
			}
			if err != nil && !errors.Is(err, k8s.ErrAppNotDeployed) {
				mu.Lock()
				errs = append(errs, fmt.Errorf("pause app %s: %w", appID, err))
				mu.Unlock()
			}
		}(app.ID, app.Status)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// mayHavePods lists the statuses PauseApp stops and records STOPPED.
func mayHavePods(status string) bool {
	return status == apphost.StatusRunning || status == apphost.StatusFailed || status == apphost.StatusPausing
}

// RestartFunctionRuntime brings back the function runtime a project deletion stopped.
func (s *AppDeployService) RestartFunctionRuntime(ctx context.Context, projectID string) error {
	namespace, err := s.namespaceFor(projectID)
	if errors.Is(err, errNoAppNamespace) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.kube.RestartFunctionRuntime(ctx, namespace)
}

// restoreRoutes serves a resumed app at its URL and custom domains again,
// which a project deletion took away. The route is the one the latest deploy
// ran, as a deploy routes it: a redeploy of an older config may differ from the record.
func (s *AppDeployService) restoreRoutes(ctx context.Context, namespace string, app *apphost.App) error {
	latest, err := s.deploys.GetLatest(app.ProjectID, app.ID)
	if err != nil {
		return fmt.Errorf("read the app's deploy history: %w", err)
	}
	if latest != nil {
		served := latest.Config.ToApp(app.ID, app.ProjectID, app.Name)
		served.Disk = app.Disk
		app = served
	}
	if err := s.kube.RestoreAppRoute(ctx, namespace, app, s.render.Route); err != nil {
		return fmt.Errorf("restore the app's route: %w", err)
	}
	if s.domainSync == nil {
		return nil
	}
	if err := s.domainSync(ctx, namespace, app); err != nil {
		return fmt.Errorf("route the app's custom domains: %w", err)
	}
	return nil
}
