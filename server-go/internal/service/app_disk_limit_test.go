package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// The owner's downgrade rule (2026-09-29): when the plan's cap drops below an
// app's disk, the disk is lowered to any size at or above what it holds and
// deploys go on; an app whose disk holds more than the cap is stopped, with the
// reason, rather than left above it.

const mebibyte = int64(1) << 20

// overPlanDeploy is an app with a deployed 2Gi disk on a plan capped at capBytes.
func overPlanDeploy(t *testing.T, capBytes, usedBytes int64) (*AppDeployService, *fakeDeployStore, *k8s.MockClient, *apphost.App) {
	t.Helper()
	app := sampleDiskApp()
	app.Disk.Size = "2Gi"
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetDiskLimits(fixedDiskLimit{maxBytes: capBytes})
	svc.SetDiskJobs(testDiskJobOptions)
	svc.render.DiskStorageClass = "excalibase-tenant"
	kube.AppDiskUsages = map[string]k8s.AppDiskUsage{
		testDeployNamespace + "/" + app.ID: {UsedBytes: usedBytes, SizeBytes: 2 << 30},
	}
	return svc, deploys, kube, app
}

var testDiskJobOptions = k8s.DiskJobOptions{Image: "busybox@sha256:x", RuntimeClass: "gvisor", Timeout: 1}

func storedDisk(t *testing.T, svc *AppDeployService, app *apphost.App) apphost.AppDisk {
	t.Helper()
	stored, err := svc.apps.Get(app.ProjectID, app.ID)
	if err != nil || stored == nil || stored.Disk == nil {
		t.Fatalf("stored app: %+v, %v", stored, err)
	}
	return *stored.Disk
}

func callIndex(calls []string, prefix string) int {
	return slices.IndexFunc(calls, func(call string) bool { return strings.HasPrefix(call, prefix) })
}

func TestDeployApp_LowersADiskAboveThePlanWhenItsDataFits(t *testing.T) {
	svc, deploys, kube, app := overPlanDeploy(t, 500*mebibyte, 100*mebibyte)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if got := deploys.getByID(deploy.ID); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("deploy %q: %s", got.Status, got.FailureReason)
	}
	want := apphost.AppDisk{MountPath: "/data", Size: "500Mi", Generation: 1}
	if got := storedDisk(t, svc, app); got != want {
		t.Fatalf("stored disk = %+v, want %+v", got, want)
	}
	if !slices.Equal(kube.AppDiskCopies, []string{testDeployNamespace + "/" + app.ID + "->500Mi@1"}) {
		t.Fatalf("copies = %v, want one onto a 500Mi generation-1 volume", kube.AppDiskCopies)
	}
	// Stopped before the copy, so nothing writes while it runs; the old volume goes last.
	pause, copyAt, apply := callIndex(kube.Calls, "PauseAppWorkload:"), callIndex(kube.Calls, "CopyAppDisk:"), callIndex(kube.Calls, "ApplyAppWorkload:")
	if pause < 0 || copyAt < pause || apply < copyAt {
		t.Fatalf("calls = %v, want pause, copy, then apply", kube.Calls)
	}
	if !slices.Contains(kube.AppDiskPruned, testDeployNamespace+"/"+app.ID+"@1") {
		t.Errorf("pruned = %v, want every volume but generation 1 removed", kube.AppDiskPruned)
	}
	workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]
	if workload == nil || workload.Disk == nil || workload.Disk.Name != k8s.AppDiskClaimName(app.ID, 1) {
		t.Fatalf("the deploy must mount the lowered volume, got %+v", workload)
	}
}

func TestDeployApp_StopsAnAppWhoseDiskHoldsMoreThanThePlan(t *testing.T) {
	svc, deploys, kube, app := overPlanDeploy(t, 1<<30, 1200*mebibyte)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	got := deploys.getByID(deploy.ID)
	if got.Status != apphost.DeployStatusFailed {
		t.Fatalf("deploy status %q, want failed", got.Status)
	}
	for _, part := range []string{"1200Mi", "1Gi", "stopped"} {
		if !strings.Contains(got.FailureReason, part) {
			t.Errorf("reason %q does not name %q", got.FailureReason, part)
		}
	}
	if status := deploys.appStatusOf(app.ID); status != apphost.StatusStopped {
		t.Errorf("app status = %q, want STOPPED", status)
	}
	if callIndex(kube.Calls, "PauseAppWorkload:") < 0 {
		t.Errorf("calls = %v, want the running app stopped", kube.Calls)
	}
	if len(kube.AppWorkloads) != 0 || len(kube.AppDiskCopies) != 0 {
		t.Error("nothing may be applied or copied for a disk that does not fit")
	}
	if got := storedDisk(t, svc, app); got.Size != "2Gi" || got.Generation != 0 {
		t.Errorf("the disk must stay as it is: %+v", got)
	}
}

// Nothing was ever written to a disk no deploy created: the record is lowered.
func TestDeployApp_ADiskNotCreatedYetIsLoweredOnTheRecord(t *testing.T) {
	svc, deploys, kube, app := overPlanDeploy(t, 1<<30, 0)
	kube.AppDiskUsages = nil

	deploy, _ := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if got := deploys.getByID(deploy.ID); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("deploy %q: %s", got.Status, got.FailureReason)
	}
	if got := storedDisk(t, svc, app); got.Size != "1Gi" || got.Generation != 0 {
		t.Errorf("stored disk = %+v, want 1Gi on generation 0", got)
	}
	if len(kube.AppDiskCopies) != 0 {
		t.Errorf("copies = %v, want none", kube.AppDiskCopies)
	}
}

func TestDeployApp_AFailedLowerKeepsTheDiskAndSaysWhy(t *testing.T) {
	cases := map[string]func(*k8s.MockClient){
		"the copy fails": func(kube *k8s.MockClient) {
			kube.AppDiskCopyErr = fmt.Errorf("%w: cp: write error: No space left on device", k8s.ErrAppDiskJob)
		},
		"the usage cannot be read": func(kube *k8s.MockClient) {
			kube.AppDiskUsageErr = fmt.Errorf("%w: probe did not finish", k8s.ErrAppDiskJob)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			svc, deploys, kube, app := overPlanDeploy(t, 500*mebibyte, 100*mebibyte)
			setup(kube)
			deploy, _ := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
			got := deploys.getByID(deploy.ID)
			if got.Status != apphost.DeployStatusFailed || !strings.Contains(got.FailureReason, "disk") {
				t.Fatalf("deploy %q %q, want a failure about the disk", got.Status, got.FailureReason)
			}
			if disk := storedDisk(t, svc, app); disk.Size != "2Gi" || disk.Generation != 0 {
				t.Errorf("the disk must stay as it is: %+v", disk)
			}
			oldClaim := testDeployNamespace + "/" + app.ID + "=" + k8s.AppDiskClaimName(app.ID, 0)
			if n := len(kube.AppDiskRepointed); (n > 0 && kube.AppDiskRepointed[n-1] != oldClaim) || len(kube.AppWorkloads) != 0 {
				t.Errorf("repointed %v, workloads %d; want the old disk kept and nothing applied", kube.AppDiskRepointed, len(kube.AppWorkloads))
			}
		})
	}
}

func TestResizeAppDisk_LowersAStoppedAppsDiskToAnySizeAtOrAboveItsUsage(t *testing.T) {
	f := diskLifecycleFixture(t)
	setAppStatus(f, apphost.StatusStopped)
	f.svc.SetDiskJobs(testDiskJobOptions)
	f.kube.AppDiskUsages = map[string]k8s.AppDiskUsage{f.key(): {UsedBytes: 100 * mebibyte, SizeBytes: 5 << 30}}

	app, err := f.svc.ResizeAppDisk(context.Background(), f.app.ProjectID, f.app.ID, "1Gi")
	if err != nil {
		t.Fatalf("ResizeAppDisk: %v", err)
	}
	want := apphost.AppDisk{MountPath: "/data", Size: "1Gi", Generation: 1}
	if *app.Disk != want || storedDisk(t, f.svc, f.app) != want {
		t.Fatalf("disk = %+v, stored %+v; want %+v", app.Disk, storedDisk(t, f.svc, f.app), want)
	}
	if n := len(f.kube.AppDiskRepointed); n == 0 || f.kube.AppDiskRepointed[n-1] != f.key()+"="+k8s.AppDiskClaimName(f.app.ID, 1) {
		t.Errorf("repointed = %v, want the stopped Deployment on the new volume", f.kube.AppDiskRepointed)
	}
	if f.status() != apphost.StatusStopped {
		t.Errorf("status = %s, want the app left stopped", f.status())
	}
	// And again, from generation 1 to 2.
	if _, err := f.svc.ResizeAppDisk(context.Background(), f.app.ProjectID, f.app.ID, "500Mi"); err != nil {
		t.Fatalf("second lower: %v", err)
	}
	if got := storedDisk(t, f.svc, f.app); got.Size != "500Mi" || got.Generation != 2 {
		t.Errorf("stored = %+v, want 500Mi on generation 2", got)
	}
}

func TestResizeAppDisk_Refusals(t *testing.T) {
	cases := map[string]struct {
		size    string
		status  string
		used    int64
		wantErr error
	}{
		"below what it holds":  {size: "500Mi", status: apphost.StatusStopped, used: 600 * mebibyte, wantErr: ErrAppDiskBelowUsage},
		"a running app":        {size: "1Gi", status: apphost.StatusRunning, used: mebibyte, wantErr: ErrAppDiskLowerNeedsStop},
		"the same size":        {size: "5Gi", status: apphost.StatusStopped, used: mebibyte, wantErr: ErrAppDiskSameSize},
		"not a size":           {size: "7.5Gi", status: apphost.StatusStopped, used: mebibyte, wantErr: apphost.ErrInvalidDisk},
		"lowered but too big":  {size: "4Gi", status: apphost.StatusStopped, used: mebibyte, wantErr: apphost.ErrDiskAbovePlan},
		"grown above the plan": {size: "21Gi", status: apphost.StatusStopped, used: mebibyte, wantErr: apphost.ErrDiskAbovePlan},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := diskLifecycleFixture(t)
			setAppStatus(f, tc.status)
			f.svc.SetDiskJobs(testDiskJobOptions)
			if name == "lowered but too big" {
				f.svc.SetDiskLimits(fixedDiskLimit{maxBytes: 3 << 30})
			}
			f.kube.AppDiskUsages = map[string]k8s.AppDiskUsage{f.key(): {UsedBytes: tc.used, SizeBytes: 5 << 30}}
			before := storedDisk(t, f.svc, f.app)
			if _, err := f.svc.ResizeAppDisk(context.Background(), f.app.ProjectID, f.app.ID, tc.size); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if storedDisk(t, f.svc, f.app) != before || len(f.kube.AppDiskCopies) != 0 {
				t.Error("a refused resize changed the disk")
			}
		})
	}
}

func setAppStatus(f *lifecycleFixture, status string) {
	f.app.Status = status
	setStoredApp(f.svc, f.app)
}

func TestResumeApp_LowersADiskAboveThePlanFirst(t *testing.T) {
	f := diskLifecycleFixture(t)
	setAppStatus(f, apphost.StatusStopped)
	f.svc.SetDiskJobs(testDiskJobOptions)
	f.svc.SetDiskLimits(fixedDiskLimit{maxBytes: 1 << 30})
	f.kube.AppDiskUsages = map[string]k8s.AppDiskUsage{f.key(): {UsedBytes: 100 * mebibyte, SizeBytes: 5 << 30}}

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev-1"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if got := storedDisk(t, f.svc, f.app); got.Size != "1Gi" || got.Generation != 1 {
		t.Errorf("stored disk = %+v, want 1Gi on generation 1", got)
	}
	repoint, resume := callIndex(f.kube.Calls, "RepointAppDisk:"), callIndex(f.kube.Calls, "ResumeAppWorkload:")
	if repoint < 0 || resume < repoint {
		t.Fatalf("calls = %v, want the Deployment repointed before it is resumed", f.kube.Calls)
	}
	if f.status() != apphost.StatusRunning {
		t.Errorf("status = %s", f.status())
	}
}

func TestResumeApp_RefusesADiskThatHoldsMoreThanThePlan(t *testing.T) {
	f := diskLifecycleFixture(t)
	setAppStatus(f, apphost.StatusStopped)
	f.svc.SetDiskJobs(testDiskJobOptions)
	f.svc.SetDiskLimits(fixedDiskLimit{maxBytes: 1 << 30})
	f.kube.AppDiskUsages = map[string]k8s.AppDiskUsage{f.key(): {UsedBytes: 3 << 30, SizeBytes: 5 << 30}}

	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev-1")
	if !errors.Is(err, ErrAppDiskUsageAbovePlan) || !strings.Contains(err.Error(), "3Gi") {
		t.Fatalf("err = %v, want ErrAppDiskUsageAbovePlan naming 3Gi", err)
	}
	if f.status() != apphost.StatusStopped || callIndex(f.kube.Calls, "ResumeAppWorkload:") >= 0 {
		t.Fatalf("status %s, calls %v; want it left stopped", f.status(), f.kube.Calls)
	}
}

func TestAppDiskStatus_ReportsUsageAgainstTheLimit(t *testing.T) {
	f := diskLifecycleFixture(t)
	f.svc.SetDiskJobs(testDiskJobOptions)
	f.kube.AppDiskUsages = map[string]k8s.AppDiskUsage{f.key(): {UsedBytes: 100 * mebibyte, SizeBytes: 5000 * mebibyte}}

	report, err := f.svc.AppDiskStatus(context.Background(), f.app.ProjectID, f.app.ID)
	if err != nil {
		t.Fatalf("AppDiskStatus: %v", err)
	}
	if report.Size != "5Gi" || report.SizeBytes != 5<<30 || report.PlanMaxBytes != 20<<30 || report.PlanMax != "20Gi" {
		t.Errorf("report = %+v", report)
	}
	if report.UsedBytes == nil || *report.UsedBytes != 100*mebibyte || report.FilesystemBytes == nil || *report.FilesystemBytes != 5000*mebibyte {
		t.Errorf("usage = %v of %v", report.UsedBytes, report.FilesystemBytes)
	}
	if report.OverPlan {
		t.Error("a 5Gi disk on a 20Gi plan is not over it")
	}
	// Not created yet: nothing measured, and not an error.
	f.kube.AppDiskUsages = nil
	report, err = f.svc.AppDiskStatus(context.Background(), f.app.ProjectID, f.app.ID)
	if err != nil || report.UsedBytes != nil {
		t.Fatalf("report %+v, %v; want no usage and no error", report, err)
	}
	f.app.Disk = nil
	setStoredApp(f.svc, f.app)
	if _, err := f.svc.AppDiskStatus(context.Background(), f.app.ProjectID, f.app.ID); !errors.Is(err, ErrAppHasNoDisk) {
		t.Fatalf("err = %v, want ErrAppHasNoDisk", err)
	}
}

// A plan change re-runs the rule on every running app whose disk the plan no
// longer allows, by redeploying what it runs; stopped apps meet it on resume.
func TestEnforceDiskCaps_RedeploysRunningAppsAboveThePlan(t *testing.T) {
	svc, deploys, kube, app := overPlanDeploy(t, 20<<30, 100*mebibyte)
	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil || deploys.getByID(first.ID).Status != apphost.DeployStatusSucceeded {
		t.Fatalf("first deploy: %v %+v", err, deploys.getByID(first.ID))
	}
	running, _ := svc.apps.Get(app.ProjectID, app.ID)
	running.Status = apphost.StatusRunning
	setStoredApp(svc, running)

	svc.EnforceDiskCaps(context.Background())
	if kube.AppDiskCopies != nil {
		t.Fatalf("a disk within the plan must be left alone, copies %v", kube.AppDiskCopies)
	}

	svc.SetDiskLimits(fixedDiskLimit{maxBytes: 500 * mebibyte})
	svc.EnforceDiskCaps(context.Background())
	latest, _ := deploys.GetLatest(app.ProjectID, app.ID)
	if latest.ID == first.ID || latest.RedeployOf != first.ID || latest.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("latest deploy = %+v, want a successful redeploy of %s", latest, first.ID)
	}
	if latest.CreatedBy != DiskCapActor {
		t.Errorf("created by %q, want %q", latest.CreatedBy, DiskCapActor)
	}
	if got := storedDisk(t, svc, app); got.Size != "500Mi" || got.Generation != 1 {
		t.Errorf("stored disk = %+v, want 500Mi on generation 1", got)
	}
}

// The disk is made (and opened to the image's user) before the workload that mounts it.
func TestDeployApp_CreatesTheDiskBeforeTheWorkload(t *testing.T) {
	svc, deploys, kube, app := overPlanDeploy(t, 20<<30, 0)
	deploy, _ := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if got := deploys.getByID(deploy.ID); got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("deploy %q: %s", got.Status, got.FailureReason)
	}
	create, apply := callIndex(kube.Calls, "CreateAppDisk:"), callIndex(kube.Calls, "ApplyAppWorkload:")
	if create < 0 || apply < create {
		t.Fatalf("calls = %v, want the disk created before the workload", kube.Calls)
	}
	kube.AppDiskCreateErr = fmt.Errorf("%w: the platform's storage budget would be exceeded", k8s.ErrAppDiskJob)
	kube.AppWorkloads = map[string]*k8s.AppWorkload{}
	deploy, _ = svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if got := deploys.getByID(deploy.ID); got.Status != apphost.DeployStatusFailed || !strings.Contains(got.FailureReason, "storage budget") {
		t.Fatalf("deploy %q %q, want a failure naming the budget", got.Status, got.FailureReason)
	}
	if len(kube.AppWorkloads) != 0 {
		t.Error("nothing may be applied without the disk")
	}
}

// A resume starts on the volume the record names, whatever the Deployment was
// last pointed at: an interrupted move must never leave the app writing to a
// volume the platform would later remove.
func TestResumeApp_MountsTheRecordedDisk(t *testing.T) {
	f := diskLifecycleFixture(t)
	f.app.Disk.Generation = 2
	setAppStatus(f, apphost.StatusStopped)
	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev-1"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	repoint, resume := callIndex(f.kube.Calls, "RepointAppDisk:"+f.key()+"="+k8s.AppDiskClaimName(f.app.ID, 2)), callIndex(f.kube.Calls, "ResumeAppWorkload:")
	if repoint < 0 || resume < repoint {
		t.Fatalf("calls = %v, want the recorded volume mounted before the resume", f.kube.Calls)
	}
}

// The new volume must hold what the old one does plus the new filesystem's own
// overhead, or the copy would fill it to the last block.
func TestResizeAppDisk_LeavesRoomForTheFilesystem(t *testing.T) {
	f := diskLifecycleFixture(t)
	setAppStatus(f, apphost.StatusStopped)
	f.svc.SetDiskJobs(testDiskJobOptions)
	f.kube.AppDiskUsages = map[string]k8s.AppDiskUsage{f.key(): {UsedBytes: 495 * mebibyte, SizeBytes: 5 << 30}}
	if _, err := f.svc.ResizeAppDisk(context.Background(), f.app.ProjectID, f.app.ID, "500Mi"); !errors.Is(err, ErrAppDiskBelowUsage) {
		t.Fatalf("err = %v, want ErrAppDiskBelowUsage", err)
	}
	if _, err := f.svc.ResizeAppDisk(context.Background(), f.app.ProjectID, f.app.ID, "600Mi"); err != nil {
		t.Fatalf("with room to spare: %v", err)
	}
}
