package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/google/uuid"
)

// Large images need time to pull.
const defaultAppRolloutTimeout = 10 * time.Minute

var errNoAppNamespace = errors.New("the project has no namespace to deploy into")

// activeRollout lets a newer deploy cancel an earlier one's rollout wait.
type activeRollout struct {
	deployID string
	revision int
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
	// stopTimeout bounds a pause's and a deletion's wait for the pods to be gone.
	stopTimeout time.Duration
	// deployLeaseWait is how long a deploy queues behind another operation on the app.
	deployLeaseWait time.Duration
	claimer         ProjectOperationClaimer
	secrets         AppSecretPurger
	// registries reads the pull credential for the image's registry; nil when no vault is configured.
	registries      RegistryCredentialLookup
	plans           PlanTiers
	headroomPercent int
	// domainSync routes the app's custom domains under the name it is deployed as.
	domainSync func(ctx context.Context, namespace string, app *apphost.App) error
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
		render:          render,
		timeout:         defaultAppRolloutTimeout,
		stopTimeout:     defaultAppStopTimeout,
		deployLeaseWait: defaultDeployLeaseWait,
		claimer:         newInProcessOperationClaimer(),
		async:           func(f func()) { go f() },
		active:          make(map[string]*activeRollout),
	}
}

// RegistryCredentialLookup answers which credential, if any, pulls from a registry.
type RegistryCredentialLookup interface {
	Lookup(projectID, registry string) (*apphost.RegistryCredential, error)
}

func (s *AppDeployService) SetRegistryCredentials(registries RegistryCredentialLookup) {
	s.registries = registries
}

// pullAuth fails rather than pull anonymously when the credential cannot be read.
func (s *AppDeployService) pullAuth(projectID, image string) (*k8s.RegistryAuth, error) {
	if s.registries == nil {
		return nil, nil
	}
	registry := apphost.ImageRegistry(image)
	cred, err := s.registries.Lookup(projectID, registry)
	if err != nil {
		return nil, fmt.Errorf("read the pull credential for %s: %w", registry, err)
	}
	if cred == nil {
		return nil, nil
	}
	return &k8s.RegistryAuth{Registry: registry, Username: cred.Username, Password: cred.Password}, nil
}

// errPullAuthChanged fails a deploy whose credential was removed or replaced while it was applied.
var errPullAuthChanged = errors.New("the registry credential was removed or replaced while the app deployed; deploy again")

// confirmPullAuth reads the credential again after the pull secret was
// written: a removal that ran in between deleted the secrets before this one
// existed, so this deploy takes its own copy away.
func (s *AppDeployService) confirmPullAuth(ctx context.Context, projectID, namespace string, used *k8s.RegistryAuth) error {
	if used == nil {
		return nil
	}
	cred, err := s.registries.Lookup(projectID, used.Registry)
	if err == nil && cred != nil && cred.Username == used.Username && cred.Password == used.Password {
		return nil
	}
	if delErr := s.kube.DeleteRegistryPullSecrets(ctx, namespace, used.Registry); delErr != nil {
		return errors.Join(errPullAuthChanged, fmt.Errorf("remove the stale pull secret: %w", delErr))
	}
	if err != nil {
		return fmt.Errorf("read the pull credential for %s: %w", used.Registry, err)
	}
	return errPullAuthChanged
}

func (s *AppDeployService) SetDomainSync(sync func(ctx context.Context, namespace string, app *apphost.App) error) {
	s.domainSync = sync
}

func (s *AppDeployService) DeployApp(ctx context.Context, projectID, appID, actor string) (*apphost.Deploy, error) {
	return s.underLease(ctx, projectID, appID, func(app *apphost.App) (*apphost.Deploy, func(), error) {
		return s.rollout(ctx, app, apphost.ConfigFromApp(app), actor, "")
	})
}

// RedeployApp rolls a deploy's frozen config out again as a new deploy. It
// never touches the app record — the source deploy's config is what runs,
// even if the app has since been edited.
func (s *AppDeployService) RedeployApp(ctx context.Context, projectID, appID, deployID, actor string) (*apphost.Deploy, error) {
	return s.underLease(ctx, projectID, appID, func(app *apphost.App) (*apphost.Deploy, func(), error) {
		source, err := s.deploys.Get(projectID, appID, deployID)
		if err != nil {
			return nil, nil, fmt.Errorf("look up deploy: %w", err)
		}
		if source == nil {
			return nil, nil, apphost.ErrDeployNotFound
		}
		return s.rollout(ctx, app, source.Config, actor, source.ID)
	})
}

// underLease applies a deploy while holding the app's lease, and starts its
// rollout watch only once the lease is released: the watch takes the lease
// again to finish.
func (s *AppDeployService) underLease(ctx context.Context, projectID, appID string,
	apply func(*apphost.App) (*apphost.Deploy, func(), error)) (*apphost.Deploy, error) {
	release, err := s.holdAppForDeploy(ctx, projectID, appID)
	if err != nil {
		return nil, err
	}
	app, err := s.lookupApp(projectID, appID)
	if err != nil {
		release()
		return nil, err
	}
	deploy, startWatch, err := apply(app)
	release()
	if startWatch != nil {
		startWatch()
	}
	return deploy, err
}

// rollout is the deploy engine DeployApp and RedeployApp both drive: create
// the record, apply the workload, and watch the rollout. cfg is what actually
// runs; redeployOf names the deploy it was frozen from, or "" for a plain
// deploy.
func (s *AppDeployService) rollout(ctx context.Context, app *apphost.App, cfg apphost.DeployConfig, actor, redeployOf string) (*apphost.Deploy, func(), error) {
	tierType, tier, err := s.planTier(ctx, app.ProjectID, cfg.Replicas)
	if err != nil {
		return nil, nil, err
	}
	cfg.Tier = tierType

	url, routeErr := s.render.Route.Public().URL(app.Name, app.ProjectID)
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
			URL:     url,
			AppName: app.Name,
		},
		Config:     cfg,
		RedeployOf: redeployOf,
		Kind:       apphost.DeployKindDeploy,
		Status:     apphost.DeployStatusPending,
		CreatedBy:  actor,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.deploys.Create(deploy); err != nil {
		return nil, nil, err
	}
	s.cancelActive(app.ID)
	namespace, err := s.namespaceFor(app.ProjectID)
	if err != nil {
		s.failWithoutWorkload(deploy, err)
		return deploy, nil, nil
	}
	name := k8s.AppObjectName(app.Name)
	if routeErr != nil {
		s.fail(ctx, deploy, routeErr, namespace, name)
		return deploy, nil, nil
	}
	if err := s.admit(ctx, namespace, deploy, tier, cfg.Replicas); err != nil {
		s.fail(ctx, deploy, err, namespace, name)
		return deploy, nil, nil
	}
	render := s.render
	render.EnvRevision = strconv.Itoa(deploy.Revision)
	render.DeployID = deploy.ID
	if render.PullAuth, err = s.pullAuth(app.ProjectID, cfg.Image); err != nil {
		s.fail(ctx, deploy, err, namespace, name)
		return deploy, nil, nil
	}
	workload, err := k8s.RenderAppWorkload(namespace, cfg.ToApp(app.ID, app.ProjectID, app.Name), s.resolver, render)
	if err != nil {
		s.fail(ctx, deploy, err, namespace, name)
		return deploy, nil, nil
	}
	if err := s.kube.ApplyAppWorkload(ctx, namespace, workload); err != nil {
		s.fail(ctx, deploy, fmt.Errorf("apply app workload: %w", err), namespace, name)
		return deploy, nil, nil
	}
	if err := s.confirmPullAuth(ctx, app.ProjectID, namespace, render.PullAuth); err != nil {
		s.fail(ctx, deploy, err, namespace, name)
		return deploy, nil, nil
	}
	if s.domainSync != nil {
		if err := s.domainSync(ctx, namespace, app); err != nil {
			s.fail(ctx, deploy, fmt.Errorf("route the app's custom domains: %w", err), namespace, name)
			return deploy, nil, nil
		}
	}
	s.setStatus(deploy, apphost.DeployStatusRolling, "", nil)
	watched := *deploy
	return deploy, func() { s.watch(ctx, watched, namespace, app.Name) }, nil
}

// watch waits out the rollout on its own copy of the deploy, so the one handed
// back to the caller is never written while it is being encoded.
func (s *AppDeployService) watch(ctx context.Context, deploy apphost.Deploy, namespace, appName string) {
	name := k8s.AppObjectName(appName)
	rolloutCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	entry := &activeRollout{deployID: deploy.ID, revision: deploy.Revision, cancel: cancel}
	s.mu.Lock()
	s.active[deploy.AppID] = entry
	s.mu.Unlock()

	timeout := s.timeout
	s.async(func() {
		defer cancel()
		defer s.clearActive(deploy.AppID, entry)
		err := s.kube.WaitForAppRollout(rolloutCtx, namespace, name, deploy.ID, timeout)
		if rolloutCtx.Err() != nil {
			return // superseded; the store would refuse this write anyway
		}
		if err != nil {
			s.fail(rolloutCtx, &deploy, err, namespace, name)
			return
		}
		s.finishRollout(rolloutCtx, &deploy, namespace, appName)
	})
}

// finishRollout prunes what the app ran under an earlier name, but only while
// holding the app's lease and only if this deploy is still the current one: a
// watch outliving a newer deploy, a pause or a deletion must not delete what
// they made. Without the lease the deploy stays rolling for the sweeper.
func (s *AppDeployService) finishRollout(ctx context.Context, deploy *apphost.Deploy, namespace, appName string) {
	release, err := s.holdAppForDeploy(ctx, deploy.ProjectID, deploy.AppID)
	if err != nil {
		log.Printf("finish deploy %s: %v", deploy.ID, err)
		return
	}
	defer release()
	current, err := s.deploys.Get(deploy.ProjectID, deploy.AppID, deploy.ID)
	if err != nil {
		log.Printf("finish deploy %s: %v", deploy.ID, err)
		return
	}
	if current == nil || !isUnfinished(current.Status) {
		return
	}
	name := k8s.AppObjectName(appName)
	if err := s.kube.PruneAppWorkload(ctx, namespace, deploy.AppID, appName, s.stopTimeout); err != nil {
		s.fail(ctx, deploy, fmt.Errorf("remove what the app ran under an earlier name: %w", err), namespace, name)
		return
	}
	s.finish(deploy, apphost.DeployStatusSucceeded, "",
		apphost.StatusAfterDeploy(true, deploy.Config.Replicas, true))
}

func isUnfinished(status string) bool {
	return status == apphost.DeployStatusPending || status == apphost.DeployStatusRolling
}

// ResumeRollouts watches every unfinished deploy this process is not already
// watching, so a deploy whose watch died with its process still ends. The
// watch runs a full timeout from here: how long the process was gone is not
// the rollout's fault.
func (s *AppDeployService) ResumeRollouts(ctx context.Context) error {
	deploys, err := s.deploys.ListUnfinished()
	if err != nil {
		return fmt.Errorf("list unfinished deploys: %w", err)
	}
	for _, deploy := range deploys {
		if s.claimResume(deploy) {
			s.resume(ctx, deploy)
		}
	}
	return nil
}

// claimResume stops a local watch of an older deploy of the same app, which a
// newer deploy made elsewhere has superseded, and leaves a current one alone.
func (s *AppDeployService) claimResume(deploy *apphost.Deploy) bool {
	s.mu.Lock()
	current, ok := s.active[deploy.AppID]
	if ok && current.revision >= deploy.Revision {
		s.mu.Unlock()
		return false
	}
	if ok {
		delete(s.active, deploy.AppID)
	}
	s.mu.Unlock()
	if ok {
		current.cancel()
	}
	return true
}

func (s *AppDeployService) resume(ctx context.Context, deploy *apphost.Deploy) {
	app, err := s.apps.Get(deploy.ProjectID, deploy.AppID)
	if err != nil {
		log.Printf("resume deploy %s: look up app: %v", deploy.ID, err)
		return
	}
	if app == nil {
		return // deleted; its deploys go with it
	}
	namespace, err := s.namespaceFor(deploy.ProjectID)
	if errors.Is(err, errNoAppNamespace) {
		s.failWithoutWorkload(deploy, err)
		return
	}
	if err != nil {
		log.Printf("resume deploy %s: %v", deploy.ID, err)
		return
	}
	if deploy.Spec.AppName == "" {
		s.finish(deploy, apphost.DeployStatusFailed, "the deploy does not record the name its workload runs under", "")
		return
	}
	s.watch(ctx, *deploy, namespace, deploy.Spec.AppName)
}

// StartRolloutSweeper resumes orphaned rollouts at once and then on a timer,
// while this replica leads, so a watch lost with a crashed replica does not
// wait for the next restart. Returns a function that stops it.
func (s *AppDeployService) StartRolloutSweeper(ctx context.Context, leader LeaderChecker, every time.Duration) func() {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			s.sweepIfLeader(ctx, leader)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return cancel
}

func (s *AppDeployService) sweepIfLeader(ctx context.Context, leader LeaderChecker) {
	leading, err := leader.IsLeader(ctx)
	if err != nil {
		log.Printf("app rollout sweep: leadership check: %v", err)
		return
	}
	if !leading {
		return
	}
	if err := s.ResumeRollouts(ctx); err != nil {
		log.Printf("app rollout sweep: %v", err)
	}
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
		return "", errNoAppNamespace
	}
	return inst.Namespace, nil
}

// fail leaves the app ACTIVE while anything, such as the previous version, is
// still serving; only a failure that left nothing serving fails the app.
func (s *AppDeployService) fail(ctx context.Context, deploy *apphost.Deploy, err error, namespace, name string) {
	s.finish(deploy, apphost.DeployStatusFailed, err.Error(), s.statusAfterFailure(ctx, namespace, name))
}

// failWithoutWorkload handles a deploy that never learnt its namespace: with
// none there is nothing serving, but a lookup that failed says nothing.
func (s *AppDeployService) failWithoutWorkload(deploy *apphost.Deploy, err error) {
	appStatus := ""
	if errors.Is(err, errNoAppNamespace) {
		appStatus = apphost.StatusFailed
	}
	s.finish(deploy, apphost.DeployStatusFailed, err.Error(), appStatus)
}

// statusAfterFailure asks the cluster rather than assuming; when it cannot
// answer, the app's status is left as it is.
func (s *AppDeployService) statusAfterFailure(ctx context.Context, namespace, name string) string {
	available, err := s.kube.AppAvailableReplicas(ctx, namespace, name)
	if err != nil {
		log.Printf("app %s/%s: read serving replicas: %v", namespace, name, err)
		return ""
	}
	return apphost.StatusAfterDeploy(false, 0, available > 0)
}

// finish records the outcome and the app status it observed together; an
// empty appStatus leaves the app as it is.
func (s *AppDeployService) finish(deploy *apphost.Deploy, status, failureReason, appStatus string) {
	now := time.Now().UTC()
	deploy.Status = status
	deploy.FailureReason = failureReason
	deploy.FinishedAt = &now
	if err := s.deploys.Finish(deploy.ID, status, failureReason, now, appStatus); err != nil {
		log.Printf("finish deploy %s as %s: %v", deploy.ID, status, err)
	}
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
