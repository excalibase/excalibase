package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// OperationAppDiskGrow holds an app's lease while its disk grows.
const OperationAppDiskGrow ProjectOperation = "app disk grow"

var (
	// ErrAppDiskDeleteUnconfirmed refuses to erase an app's disk without the caller saying so.
	ErrAppDiskDeleteUnconfirmed = errors.New("the app has a disk; deleting the app erases the disk and everything on it, so confirm it with confirmDeleteDisk")
	// ErrAppDiskShrink refuses a size at or below the current one: a volume only grows.
	ErrAppDiskShrink = errors.New("an app's disk can only grow; it cannot be shrunk")
	// ErrAppHasNoDisk refuses to grow a disk the app does not have.
	ErrAppHasNoDisk = errors.New("the app has no disk")
	errNoDiskLimits = fmt.Errorf("%w: no source for the plan's app disk cap", ErrOrgTierUnresolved)
)

// TierConfigSource reads a plan's current limits, admin edits included.
type TierConfigSource interface {
	TierConfig(ctx context.Context, tier domain.TierType) (config.TierConfig, error)
}

type appDiskLimits struct {
	plans PlanTiers
	tiers TierConfigSource
}

// NewAppDiskLimits caps an app's disk by its organisation's current plan.
func NewAppDiskLimits(plans PlanTiers, tiers TierConfigSource) apphost.DiskLimits {
	return appDiskLimits{plans: plans, tiers: tiers}
}

func (l appDiskLimits) MaxDiskGiB(ctx context.Context, projectID string) (int, error) {
	if l.plans == nil || l.tiers == nil {
		return 0, errNoDiskLimits
	}
	tier, err := l.plans.ProjectPlanTier(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	tc, err := l.tiers.TierConfig(ctx, tier)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	limit, err := apphost.PlanDiskGiB(tc.MaxAppDiskSize)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	return limit, nil
}

func (s *AppDeployService) SetDiskLimits(limits apphost.DiskLimits) { s.diskLimits = limits }

// checkDiskWithinPlan holds a disk to the plan the organisation is on now, so
// a deploy after a downgrade cannot keep a disk the plan no longer allows.
func (s *AppDeployService) checkDiskWithinPlan(ctx context.Context, app *apphost.App) error {
	if app.Disk == nil {
		return nil
	}
	if s.diskLimits == nil {
		return errNoDiskLimits
	}
	limit, err := s.diskLimits.MaxDiskGiB(ctx, app.ProjectID)
	if err != nil {
		return err
	}
	return apphost.CheckDiskWithinPlan(app.Disk, limit)
}

// GrowAppDisk grows the app's disk up to its plan's cap. The claim is grown
// first: a record left behind a grown claim is harmless, since a deploy never
// shrinks a claim, while a record ahead of it would show a size nobody has.
func (s *AppDeployService) GrowAppDisk(ctx context.Context, projectID, appID, size string) (*apphost.App, error) {
	ctx = context.WithoutCancel(ctx)
	release, err := s.holdApp(ctx, projectID, appID, OperationAppDiskGrow)
	if err != nil {
		return nil, err
	}
	defer release()
	app, err := s.lookupApp(projectID, appID)
	if err != nil {
		return nil, err
	}
	grown, err := grownDisk(app.Disk, size)
	if err != nil {
		return nil, err
	}
	resized := *app
	resized.Disk = grown
	if err := s.checkDiskWithinPlan(ctx, &resized); err != nil {
		return nil, err
	}
	if err := s.growClaim(ctx, projectID, appID, size); err != nil {
		return nil, err
	}
	if err := s.apps.Update(&resized, app.Version); err != nil {
		return nil, fmt.Errorf("record the grown disk: %w", err)
	}
	return &resized, nil
}

func grownDisk(current *apphost.AppDisk, size string) (*apphost.AppDisk, error) {
	if current == nil {
		return nil, ErrAppHasNoDisk
	}
	grown := &apphost.AppDisk{MountPath: current.MountPath, Size: size}
	want, err := grown.GiB()
	if err != nil {
		return nil, err
	}
	have, err := current.GiB()
	if err != nil {
		return nil, err
	}
	if want <= have {
		return nil, fmt.Errorf("%w: the disk is %s and %s is not larger", ErrAppDiskShrink, current.Size, size)
	}
	return grown, nil
}

// growClaim treats a disk no deploy has created as grown: the first deploy
// creates it at the recorded size.
func (s *AppDeployService) growClaim(ctx context.Context, projectID, appID, size string) error {
	namespace, err := s.namespaceFor(projectID)
	if errors.Is(err, errNoAppNamespace) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.kube.GrowAppDisk(ctx, namespace, appID, size)
	if errors.Is(err, k8s.ErrAppDiskNotCreated) {
		return nil
	}
	return err
}
