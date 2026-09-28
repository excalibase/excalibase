package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// fixedDiskLimit answers every project with one plan cap.
type fixedDiskLimit struct {
	maxGiB int
	err    error
}

func (l fixedDiskLimit) MaxDiskGiB(context.Context, string) (int, error) { return l.maxGiB, l.err }

func sampleDiskApp() *apphost.App {
	app := sampleDeployApp()
	app.ID = "app-disk1"
	app.Disk = &apphost.AppDisk{MountPath: "/data", Size: "5Gi"}
	return app
}

func newDiskDeployService(t *testing.T, app *apphost.App, maxGiB int) (*AppDeployService, *fakeDeployStore, *k8s.MockClient) {
	t.Helper()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetDiskLimits(fixedDiskLimit{maxGiB: maxGiB})
	svc.render.DiskStorageClass = "local-path"
	return svc, deploys, kube
}

func TestDeployApp_MountsTheAppsDiskOnTheTenantStorageClass(t *testing.T) {
	app := sampleDiskApp()
	svc, deploys, kube := newDiskDeployService(t, app, 20)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if got := deploys.getByID(deploy.ID); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("status %q: %s", got.Status, got.FailureReason)
	}
	workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]
	if workload == nil || workload.Disk == nil {
		t.Fatal("the app's disk must be applied with its workload")
	}
	if class := workload.Disk.Spec.StorageClassName; class == nil || *class != "local-path" {
		t.Errorf("storage class = %v, want the tenant storage class", class)
	}
	if deploy.Spec.Disk == nil || *deploy.Spec.Disk != *app.Disk {
		t.Errorf("deploy history disk = %+v, want %+v", deploy.Spec.Disk, app.Disk)
	}
}

func TestDeployApp_RefusesADiskAboveThePlan(t *testing.T) {
	app := sampleDiskApp()
	svc, _, kube := newDiskDeployService(t, app, 4)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusFailed || !strings.Contains(deploy.FailureReason, "plan allows up to 4Gi") {
		t.Fatalf("got %q %q, want a failed deploy naming the plan's cap", deploy.Status, deploy.FailureReason)
	}
	if len(kube.AppWorkloads) != 0 {
		t.Error("nothing may be applied for a disk above the plan")
	}
}

// Without a source for the plan's cap, a disk is refused rather than admitted unchecked.
func TestDeployApp_RefusesADiskWhenThePlanCapIsUnknown(t *testing.T) {
	app := sampleDiskApp()
	svc, _, kube := newDeployTestService(t, app)

	deploy, _ := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if deploy.Status != apphost.DeployStatusFailed || len(kube.AppWorkloads) != 0 {
		t.Fatalf("got %q %q with %d workloads, want a refusal", deploy.Status, deploy.FailureReason, len(kube.AppWorkloads))
	}
	svc.SetDiskLimits(fixedDiskLimit{err: errors.New("tier store down")})
	deploy, _ = svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if deploy.Status != apphost.DeployStatusFailed || len(kube.AppWorkloads) != 0 {
		t.Fatalf("got %q %q, want a refusal", deploy.Status, deploy.FailureReason)
	}
}

// A renamed app's old Deployment still holds the disk, so it goes before the
// new one is applied rather than after its rollout.
func TestDeployApp_WithADiskStopsAnEarlierNameBeforeApplying(t *testing.T) {
	app := sampleDiskApp()
	svc, _, kube := newDiskDeployService(t, app, 20)

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	prune := slices.IndexFunc(kube.Calls, func(call string) bool { return strings.HasPrefix(call, "PruneAppWorkload:") })
	apply := slices.IndexFunc(kube.Calls, func(call string) bool { return strings.HasPrefix(call, "ApplyAppWorkload:") })
	if prune < 0 || apply < 0 || prune > apply {
		t.Fatalf("calls = %v, want a prune before the apply", kube.Calls)
	}
}

func TestDeployApp_AFailedPruneStopsADiskDeploy(t *testing.T) {
	app := sampleDiskApp()
	svc, _, kube := newDiskDeployService(t, app, 20)
	kube.AppPruneErr = fmt.Errorf("%w after 3m: web-1", k8s.ErrAppPodsRemain)

	deploy, _ := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if deploy.Status != apphost.DeployStatusFailed || len(kube.AppWorkloads) != 0 {
		t.Fatalf("got %q %q, want a failed deploy with nothing applied", deploy.Status, deploy.FailureReason)
	}
}

// A redeploy of a config frozen before the disk still mounts it, and one
// frozen with several copies is refused rather than sharing the disk.
func TestRedeployApp_AlwaysRunsTheAppsCurrentDisk(t *testing.T) {
	app := sampleDeployApp()
	app.ID = "app-redeploy1"
	app.Tier = domain.Standard
	app.Replicas = 2
	svc, deploys, kube := newDiskDeployService(t, app, 20)
	svc.SetPlanTiers(fixedPlan{tier: domain.Standard})
	twoCopies := deployAs(t, svc, deploys, app)
	oneCopy := *app
	oneCopy.Replicas = 1
	oneCopyDeploy := deployAs(t, svc, deploys, &oneCopy)

	withDisk := oneCopy
	withDisk.Disk = &apphost.AppDisk{MountPath: "/data", Size: "5Gi"}
	setStoredApp(svc, &withDisk)

	redeploy, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, oneCopyDeploy.ID, "dev-2")
	if err != nil || deploys.getByID(redeploy.ID).Status != apphost.DeployStatusSucceeded {
		t.Fatalf("redeploy of a one-copy config: %v %+v", err, deploys.getByID(redeploy.ID))
	}
	if workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]; workload.Disk == nil {
		t.Fatal("a config frozen before the disk must still mount it")
	}
	refused, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, twoCopies.ID, "dev-2")
	if err != nil {
		t.Fatalf("RedeployApp: %v", err)
	}
	if refused.Status != apphost.DeployStatusFailed || !strings.Contains(refused.FailureReason, "one copy") {
		t.Fatalf("got %q %q, want a refusal naming one copy", refused.Status, refused.FailureReason)
	}
}

func deployAs(t *testing.T, svc *AppDeployService, deploys *fakeDeployStore, app *apphost.App) *apphost.Deploy {
	t.Helper()
	setStoredApp(svc, app)
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil || deploys.getByID(deploy.ID).Status != apphost.DeployStatusSucceeded {
		t.Fatalf("deploy: %v %+v", err, deploys.getByID(deploy.ID))
	}
	return deploy
}

func setStoredApp(svc *AppDeployService, app *apphost.App) {
	store := svc.apps.(*fakeAppStoreForDeploy)
	store.mu.Lock()
	defer store.mu.Unlock()
	copied := *app
	store.apps[app.ProjectID+"/"+app.ID] = &copied
}

func diskLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	f := newLifecycleFixture(t, apphost.StatusRunning)
	f.app.ID = "app-disk2"
	f.app.Disk = &apphost.AppDisk{MountPath: "/data", Size: "5Gi"}
	f.app.Version = 3
	setStoredApp(f.svc, f.app)
	f.svc.SetDiskLimits(fixedDiskLimit{maxGiB: 20})
	return f
}

func TestDeleteApp_ADiskNeedsAnExplicitConfirmation(t *testing.T) {
	f := diskLifecycleFixture(t)

	err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, false)
	if !errors.Is(err, ErrAppDiskDeleteUnconfirmed) {
		t.Fatalf("err = %v, want ErrAppDiskDeleteUnconfirmed", err)
	}
	if len(f.kube.Calls) != 0 || f.status() != apphost.StatusRunning {
		t.Fatalf("an unconfirmed deletion touched the app: calls %v, status %s", f.kube.Calls, f.status())
	}
	if err := f.svc.DeleteApp(context.Background(), f.app.ProjectID, f.app.ID, true); err != nil {
		t.Fatalf("confirmed: %v", err)
	}
	if !f.kube.AppDeleted[f.key()] {
		t.Error("the workload and its disk were not deleted")
	}
}

func TestGrowAppDisk_GrowsTheClaimThenTheRecord(t *testing.T) {
	f := diskLifecycleFixture(t)

	app, err := f.svc.GrowAppDisk(context.Background(), f.app.ProjectID, f.app.ID, "8Gi")
	if err != nil {
		t.Fatalf("GrowAppDisk: %v", err)
	}
	if !slices.Equal(f.kube.AppDiskGrown, []string{testDeployNamespace + "/" + f.app.ID + "=8Gi"}) {
		t.Errorf("grown = %v", f.kube.AppDiskGrown)
	}
	stored, _ := f.apps.Get(f.app.ProjectID, f.app.ID)
	if stored.Disk.Size != "8Gi" || app.Disk.Size != "8Gi" || stored.Disk.MountPath != "/data" {
		t.Errorf("record = %+v, returned %+v; want 8Gi at /data", stored.Disk, app.Disk)
	}
}

// Not deployed yet: the record is the disk, and the first deploy creates it at that size.
func TestGrowAppDisk_BeforeTheFirstDeployGrowsTheRecordOnly(t *testing.T) {
	f := diskLifecycleFixture(t)
	f.kube.AppDiskGrowErr = k8s.ErrAppDiskNotCreated

	if _, err := f.svc.GrowAppDisk(context.Background(), f.app.ProjectID, f.app.ID, "8Gi"); err != nil {
		t.Fatalf("GrowAppDisk: %v", err)
	}
	if stored, _ := f.apps.Get(f.app.ProjectID, f.app.ID); stored.Disk.Size != "8Gi" {
		t.Errorf("record size = %s, want 8Gi", stored.Disk.Size)
	}
}

func TestGrowAppDisk_Refusals(t *testing.T) {
	cases := map[string]struct {
		size    string
		setup   func(*lifecycleFixture)
		wantErr error
	}{
		"not whole gibibytes": {size: "7.5Gi", wantErr: apphost.ErrInvalidDisk},
		"the same size":       {size: "5Gi", wantErr: ErrAppDiskShrink},
		"smaller":             {size: "4Gi", wantErr: ErrAppDiskShrink},
		"above the plan":      {size: "21Gi", wantErr: apphost.ErrDiskAbovePlan},
		"no disk": {size: "8Gi", wantErr: ErrAppHasNoDisk, setup: func(f *lifecycleFixture) {
			f.app.Disk = nil
			setStoredApp(f.svc, f.app)
		}},
		"storage that cannot grow": {size: "8Gi", wantErr: k8s.ErrAppDiskNotExpandable, setup: func(f *lifecycleFixture) {
			f.kube.AppDiskGrowErr = fmt.Errorf("%w: storage class %q does not allow volume expansion", k8s.ErrAppDiskNotExpandable, "local-path")
		}},
		"an unknown plan cap": {size: "8Gi", wantErr: ErrOrgTierUnresolved, setup: func(f *lifecycleFixture) {
			f.svc.SetDiskLimits(fixedDiskLimit{err: fmt.Errorf("%w: tier store down", ErrOrgTierUnresolved)})
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := diskLifecycleFixture(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			before, _ := f.apps.Get(f.app.ProjectID, f.app.ID)
			if _, err := f.svc.GrowAppDisk(context.Background(), f.app.ProjectID, f.app.ID, tc.size); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			after, _ := f.apps.Get(f.app.ProjectID, f.app.ID)
			if after.Version != before.Version {
				t.Error("a refused grow changed the record")
			}
		})
	}
}

func TestGrowAppDisk_HoldsTheAppsLease(t *testing.T) {
	f := diskLifecycleFixture(t)
	release, err := f.svc.holdApp(context.Background(), f.app.ProjectID, f.app.ID, OperationPause)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := f.svc.GrowAppDisk(context.Background(), f.app.ProjectID, f.app.ID, "8Gi"); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("err = %v, want ErrProjectOperationRunning", err)
	}
}

type fixedTierConfigs map[domain.TierType]config.TierConfig

func (f fixedTierConfigs) TierConfig(_ context.Context, tier domain.TierType) (config.TierConfig, error) {
	tc, ok := f[tier]
	if !ok {
		return config.TierConfig{}, fmt.Errorf("no tier %s", tier)
	}
	return tc, nil
}

func TestAppDiskLimits_ReadTheOrganisationsPlan(t *testing.T) {
	limits := NewAppDiskLimits(fixedPlan{tier: domain.Standard},
		fixedTierConfigs{domain.Standard: {MaxAppDiskSize: "20Gi"}, domain.Free: {MaxAppDiskSize: "1Gi"}})
	if got, err := limits.MaxDiskGiB(context.Background(), "p1"); err != nil || got != 20 {
		t.Fatalf("MaxDiskGiB = %d, %v; want 20", got, err)
	}
	for name, limits := range map[string]apphost.DiskLimits{
		"no plan":            NewAppDiskLimits(fixedPlan{err: ErrOrgTierUnresolved}, fixedTierConfigs{}),
		"no tier row":        NewAppDiskLimits(fixedPlan{tier: domain.Enterprise}, fixedTierConfigs{}),
		"an unreadable cap":  NewAppDiskLimits(fixedPlan{tier: domain.Free}, fixedTierConfigs{domain.Free: {MaxAppDiskSize: "lots"}}),
		"no tier config set": NewAppDiskLimits(fixedPlan{tier: domain.Free}, nil),
	} {
		if _, err := limits.MaxDiskGiB(context.Background(), "p1"); !errors.Is(err, ErrOrgTierUnresolved) {
			t.Errorf("%s: err = %v, want ErrOrgTierUnresolved", name, err)
		}
	}
}
