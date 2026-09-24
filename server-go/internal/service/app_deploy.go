package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/google/uuid"
)

const defaultAppRolloutTimeout = 5 * time.Minute

// activeRollout lets a newer deploy cancel an earlier one's rollout wait.
type activeRollout struct {
	deployID string
	cancel   context.CancelFunc
}

type AppDeployService struct {
	apps      apphost.Store
	deploys   apphost.DeployStore
	kube      k8s.KubeClient
	instances storage.InstanceStore
	resolver  k8s.Resolver
	// render carries the sandbox runtime class and extra egress deny ranges.
	render  k8s.AppRenderOptions
	timeout time.Duration
	// async lets tests run the rollout wait inline instead of in a goroutine.
	async func(func())

	mu     sync.Mutex
	active map[string]*activeRollout // keyed by app id
}

func NewAppDeployService(
	apps apphost.Store,
	deploys apphost.DeployStore,
	kube k8s.KubeClient,
	instances storage.InstanceStore,
	resolver k8s.Resolver,
	render k8s.AppRenderOptions,
) *AppDeployService {
	return &AppDeployService{
		apps: apps, deploys: deploys, kube: kube, instances: instances, resolver: resolver,
		render:  render,
		timeout: defaultAppRolloutTimeout,
		async:   func(f func()) { go f() },
		active:  make(map[string]*activeRollout),
	}
}

func (s *AppDeployService) DeployApp(ctx context.Context, projectID, appID, actor string) (*apphost.Deploy, error) {
	app, err := s.apps.Get(projectID, appID)
	if err != nil {
		return nil, fmt.Errorf("look up app: %w", err)
	}
	if app == nil {
		return nil, apphost.ErrAppNotFound
	}
	return s.rollout(ctx, app, apphost.ConfigFromApp(app), actor, "")
}

// RedeployApp rolls a deploy's frozen config out again as a new deploy. It
// never touches the app record — the source deploy's config is what runs,
// even if the app has since been edited.
func (s *AppDeployService) RedeployApp(ctx context.Context, projectID, appID, deployID, actor string) (*apphost.Deploy, error) {
	app, err := s.apps.Get(projectID, appID)
	if err != nil {
		return nil, fmt.Errorf("look up app: %w", err)
	}
	if app == nil {
		return nil, apphost.ErrAppNotFound
	}
	source, err := s.deploys.Get(projectID, appID, deployID)
	if err != nil {
		return nil, fmt.Errorf("look up deploy: %w", err)
	}
	if source == nil {
		return nil, apphost.ErrDeployNotFound
	}
	return s.rollout(ctx, app, source.Config, actor, source.ID)
}

// rollout is the deploy engine DeployApp and RedeployApp both drive: create
// the record, apply the workload, and watch the rollout. cfg is what actually
// runs; redeployOf names the deploy it was frozen from, or "" for a plain
// deploy.
func (s *AppDeployService) rollout(ctx context.Context, app *apphost.App, cfg apphost.DeployConfig, actor, redeployOf string) (*apphost.Deploy, error) {
	tier, err := config.GetAppTierConfig(cfg.Tier)
	if err != nil {
		return nil, fmt.Errorf("resolve app tier: %w", err)
	}

	deploy := &apphost.Deploy{
		ID:        uuid.NewString(),
		AppID:     app.ID,
		ProjectID: app.ProjectID,
		Image:     cfg.Image,
		Spec: apphost.DeploySpec{
			Image:    cfg.Image,
			Env:      apphost.EnvSummaries(cfg.Env),
			Port:     cfg.Port,
			Replicas: cfg.Replicas,
			Resources: apphost.DeployResources{
				CPURequest: tier.CPURequest, CPULimit: tier.CPULimit,
				MemoryRequest: tier.MemoryRequest, MemoryLimit: tier.MemoryLimit,
			},
		},
		Config:     cfg,
		RedeployOf: redeployOf,
		Status:     apphost.DeployStatusPending,
		CreatedBy:  actor,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.deploys.Create(deploy); err != nil {
		return nil, err
	}
	s.cancelActive(app.ID)

	namespace, err := s.namespaceFor(app.ProjectID)
	if err != nil {
		s.fail(deploy, err)
		return deploy, nil
	}
	workload, err := k8s.RenderAppWorkload(namespace, cfg.ToApp(app.ID, app.ProjectID, app.Name), s.resolver, s.render)
	if err != nil {
		s.fail(deploy, err)
		return deploy, nil
	}
	if err := s.kube.ApplyAppWorkload(ctx, namespace, workload); err != nil {
		s.fail(deploy, fmt.Errorf("apply app workload: %w", err))
		return deploy, nil
	}
	s.setStatus(deploy, apphost.DeployStatusRolling, "", nil)

	name := k8s.AppObjectName(app.Name)
	timeout := s.timeout
	rolloutCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	entry := &activeRollout{deployID: deploy.ID, cancel: cancel}
	s.mu.Lock()
	s.active[app.ID] = entry
	s.mu.Unlock()

	s.async(func() {
		defer s.clearActive(app.ID, entry)
		err := s.kube.WaitForAppRollout(rolloutCtx, namespace, name, timeout)
		if rolloutCtx.Err() != nil {
			return // superseded; the store would refuse this write anyway
		}
		if err != nil {
			s.fail(deploy, err)
			return
		}
		s.setStatus(deploy, apphost.DeployStatusSucceeded, "", timeNowPtr())
	})

	return deploy, nil
}

func (s *AppDeployService) cancelActive(appID string) {
	s.mu.Lock()
	prev, ok := s.active[appID]
	if ok {
		delete(s.active, appID)
	}
	s.mu.Unlock()
	if ok {
		prev.cancel()
	}
}

func (s *AppDeployService) clearActive(appID string, entry *activeRollout) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active[appID] == entry {
		delete(s.active, appID)
	}
}

func (s *AppDeployService) ListDeploys(projectID, appID string, limit int) ([]*apphost.Deploy, error) {
	app, err := s.apps.Get(projectID, appID)
	if err != nil {
		return nil, fmt.Errorf("look up app: %w", err)
	}
	if app == nil {
		return nil, apphost.ErrAppNotFound
	}
	deploys, err := s.deploys.ListByApp(projectID, appID, limit)
	if err != nil {
		return nil, err
	}
	if deploys == nil {
		deploys = []*apphost.Deploy{}
	}
	return deploys, nil
}

func (s *AppDeployService) namespaceFor(projectID string) (string, error) {
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return "", fmt.Errorf("look up project namespace: %w", err)
	}
	if inst == nil || inst.Namespace == "" {
		return "", errors.New("the project has no namespace to deploy into")
	}
	return inst.Namespace, nil
}

func (s *AppDeployService) fail(deploy *apphost.Deploy, err error) {
	s.setStatus(deploy, apphost.DeployStatusFailed, err.Error(), timeNowPtr())
}

// Applies only from pending/rolling, so a late write loses instead of
// flipping a terminal status back over.
func (s *AppDeployService) setStatus(deploy *apphost.Deploy, status, failureReason string, finishedAt *time.Time) {
	deploy.Status = status
	deploy.FailureReason = failureReason
	deploy.FinishedAt = finishedAt
	if err := s.deploys.UpdateStatus(deploy.ID, status, failureReason, finishedAt); err != nil {
		log.Printf("update deploy %s status to %s: %v", deploy.ID, status, err)
	}
}

func timeNowPtr() *time.Time {
	now := time.Now().UTC()
	return &now
}
