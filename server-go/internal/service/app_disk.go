package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const (
	// OperationAppDiskResize holds an app's lease while its disk grows or is lowered.
	OperationAppDiskResize ProjectOperation = "app disk resize"
	// OperationAppDiskUsage holds it while the disk's usage is read.
	OperationAppDiskUsage ProjectOperation = "app disk usage"
	// DiskCapActor is who a redeploy made by a plan change is recorded as.
	DiskCapActor = "platform: plan disk cap"
	// recentDeploysScanned bounds the search for the deploy a plan change runs again.
	recentDeploysScanned = 50
	// A new ext4 volume spends part of itself on its journal and inode tables.
	filesystemOverheadMin     = 16 << 20
	filesystemOverheadPercent = 5
)

// fitsOn reports whether what a disk holds fits a new volume of size bytes,
// leaving the new filesystem its own overhead.
func fitsOn(usedBytes, sizeBytes int64) bool {
	overhead := max(int64(filesystemOverheadMin), sizeBytes*filesystemOverheadPercent/100)
	return usedBytes+overhead <= sizeBytes
}

var (
	// ErrAppDiskDeleteUnconfirmed refuses to erase an app's disk without the caller saying so.
	ErrAppDiskDeleteUnconfirmed = errors.New("the app has a disk; deleting the app erases the disk and everything on it, so confirm it with confirmDeleteDisk")
	// ErrAppDiskSameSize refuses a resize to the size the disk already has.
	ErrAppDiskSameSize = errors.New("the disk is already that size")
	// ErrAppHasNoDisk refuses to resize a disk the app does not have.
	ErrAppHasNoDisk = errors.New("the app has no disk")
	// ErrAppDiskBelowUsage refuses a smaller disk than what the disk holds.
	ErrAppDiskBelowUsage = errors.New("a disk cannot be made smaller than what it holds")
	// ErrAppDiskUsageAbovePlan: the disk holds more than the plan's cap, so it
	// cannot be lowered to fit; the app is stopped rather than left above it.
	ErrAppDiskUsageAbovePlan = errors.New("the app's disk holds more than the plan allows")
	// ErrAppDiskLowerNeedsStop refuses to lower a running app's disk.
	ErrAppDiskLowerNeedsStop = errors.New("stop the app before making its disk smaller: the disk is copied onto a smaller volume while nothing writes to it")
	errNoDiskLimits          = fmt.Errorf("%w: no source for the plan's app disk cap", ErrOrgTierUnresolved)
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

func (l appDiskLimits) MaxDiskBytes(ctx context.Context, projectID string) (int64, error) {
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
	limit, err := apphost.PlanDiskBytes(tc.MaxAppDiskSize)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	return limit, nil
}

func (s *AppDeployService) SetDiskLimits(limits apphost.DiskLimits) { s.diskLimits = limits }

// SetDiskJobs sets how the usage probe and the lowering copy run.
func (s *AppDeployService) SetDiskJobs(opts k8s.DiskJobOptions) { s.diskJobs = opts }

func (s *AppDeployService) planDiskBytes(ctx context.Context, projectID string) (int64, error) {
	if s.diskLimits == nil {
		return 0, errNoDiskLimits
	}
	return s.diskLimits.MaxDiskBytes(ctx, projectID)
}

// fitDiskToPlan applies the owner's downgrade rule (2026-09-29) to a disk above
// the plan the organisation is on now: it is lowered to the plan's cap when
// what it holds fits, and ErrAppDiskUsageAbovePlan when it does not.
func (s *AppDeployService) fitDiskToPlan(ctx context.Context, namespace string, app *apphost.App) (*apphost.App, error) {
	if app.Disk == nil {
		return app, nil
	}
	limit, err := s.planDiskBytes(ctx, app.ProjectID)
	if err != nil {
		return nil, err
	}
	size, err := app.Disk.Bytes()
	if err != nil {
		return nil, err
	}
	if size <= limit {
		return app, nil
	}
	// Stopped first: the volume admits one pod, so it is measured (and copied) without the app.
	if err := s.stopForDisk(ctx, namespace, app.ID); err != nil {
		return nil, err
	}
	used, created, err := s.diskUsed(ctx, namespace, app)
	if err != nil {
		return nil, err
	}
	target := apphost.AppDisk{MountPath: app.Disk.MountPath, Size: apphost.FormatDiskSize(limit), Generation: app.Disk.Generation}
	if limit < apphost.MinDiskBytes {
		if !created {
			return nil, apphost.CheckDiskWithinPlan(app.Disk, limit)
		}
		return nil, fmt.Errorf("%w: the plan offers no app disks and the disk holds %s", ErrAppDiskUsageAbovePlan, apphost.FormatDiskSize(used))
	}
	if !created {
		return s.recordDisk(app, target)
	}
	if !fitsOn(used, limit) {
		return nil, fmt.Errorf("%w: it holds %s and the plan allows %s", ErrAppDiskUsageAbovePlan, apphost.FormatDiskSize(used), target.Size)
	}
	target.Generation++
	return s.moveDisk(ctx, namespace, app, target)
}

// diskUsed reads what the disk holds; created is false when no deploy has made it.
func (s *AppDeployService) diskUsed(ctx context.Context, namespace string, app *apphost.App) (int64, bool, error) {
	usage, err := s.runtime.AppDiskUsage(ctx, namespace, app.ID, *app.Disk, s.diskJobs)
	if errors.Is(err, k8s.ErrAppDiskNotCreated) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read how much of the app's disk is used: %w", err)
	}
	return usage.UsedBytes, true, nil
}

// moveDisk copies a stopped app's disk onto a smaller volume and switches the
// app to it. The record changes only once the copy is complete, so a failure
// at any step leaves the app on its old disk; a copy left behind is removed by
// the next move.
func (s *AppDeployService) moveDisk(ctx context.Context, namespace string, app *apphost.App, target apphost.AppDisk) (*apphost.App, error) {
	if err := s.stopForDisk(ctx, namespace, app.ID); err != nil {
		return nil, err
	}
	// The record is the authority: an earlier move that stopped part way may have repointed the Deployment.
	oldClaim := k8s.AppDiskClaimName(app.ID, app.Disk.Generation)
	if err := s.runtime.RepointAppDisk(ctx, namespace, app.ID, oldClaim); err != nil {
		return nil, fmt.Errorf("mount the app's recorded disk: %w", err)
	}
	if err := s.runtime.DeleteOtherAppDisks(ctx, namespace, app.ID, app.Disk.Generation, s.diskJobs.Timeout); err != nil {
		return nil, fmt.Errorf("remove an unfinished copy of the app's disk: %w", err)
	}
	if err := s.runtime.CopyAppDisk(ctx, namespace, app, target, s.render.DiskStorageClass, s.diskJobs); err != nil {
		return nil, fmt.Errorf("copy the app's disk onto a %s volume: %w", target.Size, err)
	}
	if err := s.runtime.RepointAppDisk(ctx, namespace, app.ID, k8s.AppDiskClaimName(app.ID, target.Generation)); err != nil {
		return nil, s.repointBack(ctx, namespace, app.ID, oldClaim, fmt.Errorf("mount the app's smaller disk: %w", err))
	}
	moved, err := s.recordDisk(app, target)
	if err != nil {
		return nil, s.repointBack(ctx, namespace, app.ID, oldClaim, err)
	}
	if err := s.runtime.DeleteOtherAppDisks(ctx, namespace, app.ID, target.Generation, s.diskJobs.Timeout); err != nil {
		log.Printf("app %s/%s: the disk moved to %s but its old volume was not removed; the next resize removes it: %v",
			app.ProjectID, app.ID, target.Size, err)
	}
	return moved, nil
}

// stopForDisk scales the app to zero and waits until no pod holds its disk.
func (s *AppDeployService) stopForDisk(ctx context.Context, namespace, appID string) error {
	if err := s.runtime.PauseAppWorkload(ctx, namespace, appID); err != nil && !errors.Is(err, k8s.ErrAppNotDeployed) {
		return fmt.Errorf("stop the app before its disk is measured or copied: %w", err)
	}
	if err := s.runtime.WaitForAppPodsGone(ctx, namespace, appID, s.stopTimeout); err != nil {
		return fmt.Errorf("stop the app before its disk is measured or copied: %w", err)
	}
	return nil
}

func (s *AppDeployService) repointBack(ctx context.Context, namespace, appID, claim string, cause error) error {
	if err := s.runtime.RepointAppDisk(ctx, namespace, appID, claim); err != nil {
		return errors.Join(cause, fmt.Errorf("mount the app's old disk again: %w", err))
	}
	return cause
}

func (s *AppDeployService) recordDisk(app *apphost.App, disk apphost.AppDisk) (*apphost.App, error) {
	updated := *app
	updated.Disk = &disk
	if err := s.apps.Update(&updated, app.Version); err != nil {
		return nil, fmt.Errorf("record the app's disk: %w", err)
	}
	updated.Version = app.Version + 1
	return &updated, nil
}

// stopOverPlan stops an app whose disk holds more than its plan allows and
// fails the deploy with the reason, rather than run it above the cap.
func (s *AppDeployService) stopOverPlan(ctx context.Context, deploy *apphost.Deploy, namespace, name string, cause error) {
	if err := s.stopForDisk(ctx, namespace, deploy.AppID); err != nil {
		s.fail(ctx, deploy, errors.Join(cause, err), namespace, name)
		return
	}
	reason := cause.Error() + "; the app was stopped. Free space on the disk or move the organization to a larger plan, then deploy again"
	s.finish(deploy, apphost.DeployStatusFailed, reason, apphost.StatusStopped)
}

// ResizeAppDisk grows the app's disk up to its plan's cap, or lowers a stopped
// app's disk to any size at or above what it holds. Growing asks the claim
// first: a record behind a grown claim is harmless, one ahead of it would show
// a size nobody has.
func (s *AppDeployService) ResizeAppDisk(ctx context.Context, projectID, appID, size string) (*apphost.App, error) {
	ctx = context.WithoutCancel(ctx)
	release, err := s.holdApp(ctx, projectID, appID, OperationAppDiskResize)
	if err != nil {
		return nil, err
	}
	defer release()
	app, err := s.lookupApp(projectID, appID)
	if err != nil {
		return nil, err
	}
	if app.Disk == nil {
		return nil, ErrAppHasNoDisk
	}
	target := apphost.AppDisk{MountPath: app.Disk.MountPath, Size: size, Generation: app.Disk.Generation}
	want, err := target.Bytes()
	if err != nil {
		return nil, err
	}
	have, err := app.Disk.Bytes()
	if err != nil {
		return nil, err
	}
	if want == have {
		return nil, fmt.Errorf("%w: %s", ErrAppDiskSameSize, app.Disk.Size)
	}
	limit, err := s.planDiskBytes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if err := apphost.CheckDiskWithinPlan(&target, limit); err != nil {
		return nil, err
	}
	if want > have {
		return s.growDisk(ctx, app, target)
	}
	return s.lowerDisk(ctx, app, target)
}

func (s *AppDeployService) growDisk(ctx context.Context, app *apphost.App, target apphost.AppDisk) (*apphost.App, error) {
	if err := s.growClaim(ctx, app, target.Size); err != nil {
		return nil, err
	}
	return s.recordDisk(app, target)
}

func (s *AppDeployService) lowerDisk(ctx context.Context, app *apphost.App, target apphost.AppDisk) (*apphost.App, error) {
	if app.Status == apphost.StatusRunning || apphost.IsBusy(app.Status) {
		return nil, ErrAppDiskLowerNeedsStop
	}
	namespace, err := s.namespaceFor(app.ProjectID)
	if errors.Is(err, errNoAppNamespace) {
		return s.recordDisk(app, target)
	}
	if err != nil {
		return nil, err
	}
	if err := s.stopForDisk(ctx, namespace, app.ID); err != nil {
		return nil, err
	}
	used, created, err := s.diskUsed(ctx, namespace, app)
	if err != nil {
		return nil, err
	}
	if !created {
		return s.recordDisk(app, target)
	}
	want, _ := target.Bytes()
	if !fitsOn(used, want) {
		return nil, fmt.Errorf("%w: it holds %s, and a new volume also needs room for its filesystem", ErrAppDiskBelowUsage, apphost.FormatDiskSize(used))
	}
	target.Generation++
	return s.moveDisk(ctx, namespace, app, target)
}

// growClaim treats a disk no deploy has created as grown: the first deploy
// creates it at the recorded size.
func (s *AppDeployService) growClaim(ctx context.Context, app *apphost.App, size string) error {
	namespace, err := s.namespaceFor(app.ProjectID)
	if errors.Is(err, errNoAppNamespace) {
		return nil
	}
	if err != nil {
		return err
	}
	err = s.runtime.GrowAppDisk(ctx, namespace, app.ID, app.Disk.Generation, size)
	if errors.Is(err, k8s.ErrAppDiskNotCreated) {
		return nil
	}
	return err
}

// AppDiskReport is an app's disk against its plan. UsedBytes and
// FilesystemBytes are what the volume's filesystem reports, absent until a
// deploy has created the disk.
type AppDiskReport struct {
	MountPath       string     `json:"mountPath"`
	Size            string     `json:"size"`
	SizeBytes       int64      `json:"sizeBytes"`
	UsedBytes       *int64     `json:"usedBytes,omitempty"`
	FilesystemBytes *int64     `json:"filesystemBytes,omitempty"`
	PlanMax         string     `json:"planMax"`
	PlanMaxBytes    int64      `json:"planMaxBytes"`
	OverPlan        bool       `json:"overPlan"`
	MeasuredAt      *time.Time `json:"measuredAt,omitempty"`
}

// AppDiskStatus measures the app's disk. It takes the app's lease, so a
// measurement never races a resize.
func (s *AppDeployService) AppDiskStatus(ctx context.Context, projectID, appID string) (*AppDiskReport, error) {
	release, err := s.holdApp(ctx, projectID, appID, OperationAppDiskUsage)
	if err != nil {
		return nil, err
	}
	defer release()
	app, err := s.lookupApp(projectID, appID)
	if err != nil {
		return nil, err
	}
	if app.Disk == nil {
		return nil, ErrAppHasNoDisk
	}
	size, err := app.Disk.Bytes()
	if err != nil {
		return nil, err
	}
	limit, err := s.planDiskBytes(ctx, projectID)
	if err != nil {
		return nil, err
	}
	report := &AppDiskReport{
		MountPath: app.Disk.MountPath, Size: app.Disk.Size, SizeBytes: size,
		PlanMax: apphost.FormatDiskSize(limit), PlanMaxBytes: limit, OverPlan: size > limit,
	}
	namespace, err := s.namespaceFor(projectID)
	if errors.Is(err, errNoAppNamespace) {
		return report, nil
	}
	if err != nil {
		return nil, err
	}
	usage, err := s.runtime.AppDiskUsage(ctx, namespace, app.ID, *app.Disk, s.diskJobs)
	if errors.Is(err, k8s.ErrAppDiskNotCreated) {
		return report, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read how much of the app's disk is used: %w", err)
	}
	now := time.Now().UTC()
	report.UsedBytes, report.FilesystemBytes, report.MeasuredAt = &usage.UsedBytes, &usage.SizeBytes, &now
	return report, nil
}

// EnforceDiskCaps runs the downgrade rule on every running app whose disk its
// plan no longer allows, by redeploying what it runs: the deploy lowers the
// disk or stops the app. Stopped apps meet the rule when they resume.
func (s *AppDeployService) EnforceDiskCaps(ctx context.Context) {
	if !s.enforcing.TryLock() {
		return // a sweep is running; it reads the plans as they are now
	}
	defer s.enforcing.Unlock()
	instances, err := s.instances.FindAll()
	if err != nil {
		log.Printf("app disk caps: list projects: %v", err)
		return
	}
	seen := map[string]bool{}
	for _, inst := range instances {
		if seen[inst.ProjectID] {
			continue
		}
		seen[inst.ProjectID] = true
		apps, err := s.apps.List(inst.ProjectID)
		if err != nil {
			log.Printf("app disk caps: list apps of %s: %v", inst.ProjectID, err)
			continue
		}
		for _, app := range apps {
			s.enforceDiskCap(ctx, app)
		}
	}
}

func (s *AppDeployService) enforceDiskCap(ctx context.Context, app *apphost.App) {
	if app.Disk == nil || app.Status != apphost.StatusRunning {
		return
	}
	limit, err := s.planDiskBytes(ctx, app.ProjectID)
	if err != nil {
		log.Printf("app disk caps: %s/%s: %v", app.ProjectID, app.ID, err)
		return
	}
	if size, err := app.Disk.Bytes(); err == nil && size <= limit {
		return
	}
	running, err := s.lastSucceededDeploy(app)
	if err != nil || running == nil {
		log.Printf("app disk caps: %s/%s: no deploy to run again (%v)", app.ProjectID, app.ID, err)
		return
	}
	if _, err := s.RedeployApp(ctx, app.ProjectID, app.ID, running.ID, DiskCapActor); err != nil {
		log.Printf("app disk caps: %s/%s: redeploy: %v", app.ProjectID, app.ID, err)
	}
}

func (s *AppDeployService) lastSucceededDeploy(app *apphost.App) (*apphost.Deploy, error) {
	deploys, err := s.deploys.ListByApp(app.ProjectID, app.ID, recentDeploysScanned)
	if err != nil {
		return nil, err
	}
	for _, deploy := range deploys {
		if deploy.Status == apphost.DeployStatusSucceeded && deploy.Kind == apphost.DeployKindDeploy {
			return deploy, nil
		}
	}
	return nil, nil
}
