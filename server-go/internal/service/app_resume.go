package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/google/uuid"
)

var resumeRefusals = []error{ErrAppOverPlan, ErrOrgTierUnresolved, ErrAppCapacity, ErrAppNoSandboxNode, k8s.ErrAppNotPaused, k8s.ErrAppNotDeployed}

// diskRefusals leave a resume undone: the app stays as it was, on its old disk.
var diskRefusals = []error{ErrAppDiskUsageAbovePlan, apphost.ErrDiskAbovePlan, ErrOrgTierUnresolved, k8s.ErrAppDiskJob, storagebudget.ErrExceeded}

// resumeSize is what a resume brings back: the paused copies at the plan's size now.
type resumeSize struct {
	tier     domain.TierType
	pod      config.AppTierConfig
	replicas int
}

// admitResume holds a resume to the plan and the room a deploy is held to.
// The pods come back at the organisation's current plan size, so that is the size admitted.
func (s *AppDeployService) admitResume(ctx context.Context, namespace string, app *apphost.App) (resumeSize, error) {
	replicas, err := s.kube.PausedAppReplicas(ctx, namespace, app.ID, app.Name)
	if err != nil {
		return resumeSize{}, err
	}
	tierType, tier, err := s.planTier(ctx, app.ProjectID, replicas)
	var over *overPlanError
	if errors.As(err, &over) {
		return resumeSize{}, fmt.Errorf("%w and the app was paused with %d; scale it down to %d or redeploy it", err, replicas, over.allowed)
	}
	if err != nil {
		return resumeSize{}, err
	}
	if err := s.admit(ctx, namespace, &apphost.Deploy{AppID: app.ID}, tier, replicas); err != nil {
		return resumeSize{}, err
	}
	return resumeSize{tier: tierType, pod: tier, replicas: replicas}, nil
}

// recordResize writes the size down before it is applied, so the history and
// the app record never show less than the cluster may already run.
func (s *AppDeployService) recordResize(app *apphost.App, size resumeSize, actor string) error {
	latest, err := s.deploys.GetLatest(app.ProjectID, app.ID)
	if err != nil {
		return fmt.Errorf("read the app's deploy history: %w", err)
	}
	if app.Tier == size.tier && (latest == nil || latest.Config.Tier == size.tier) {
		return nil
	}
	if err := s.deploys.RecordResize(resizeEntry(app, latest, size, actor)); err != nil {
		return fmt.Errorf("record the resize: %w", err)
	}
	return nil
}

func resizeEntry(app *apphost.App, latest *apphost.Deploy, size resumeSize, actor string) *apphost.Deploy {
	base := latest
	if base == nil {
		base = &apphost.Deploy{
			Image:  app.Image,
			Config: apphost.ConfigFromApp(app),
			Spec:   apphost.DeploySpec{Image: app.Image, Env: apphost.EnvSummaries(app.Env), Port: app.Port},
		}
	}
	cfg := base.Config
	cfg.Tier, cfg.Replicas = size.tier, size.replicas
	spec := base.Spec
	spec.Replicas, spec.AppName = size.replicas, app.Name
	spec.Resources = apphost.DeployResources{
		CPURequest: size.pod.CPURequest, CPULimit: size.pod.CPULimit,
		MemoryRequest: size.pod.MemoryRequest, MemoryLimit: size.pod.MemoryLimit,
	}
	now := time.Now().UTC()
	return &apphost.Deploy{
		ID: uuid.NewString(), AppID: app.ID, ProjectID: app.ProjectID,
		Image: base.Image, Spec: spec, Config: cfg,
		Kind: apphost.DeployKindResize, Status: apphost.DeployStatusSucceeded,
		CreatedBy: actor, CreatedAt: now, FinishedAt: &now,
	}
}
