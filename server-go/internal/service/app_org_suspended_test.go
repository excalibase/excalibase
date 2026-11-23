package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const suspendedTestOrg = "org-owner-suspended"

// guardFor wires the service to a lookup that names suspended as the orgs
// whose owner is suspended, and puts the app's project in suspendedTestOrg.
func guardFor(t *testing.T, svc *AppDeployService, projectID string, suspended ...string) {
	t.Helper()
	instances := svc.instances.(*fakestore.Instances)
	instances.Items[projectID].OrgID = suspendedTestOrg
	set := map[string]bool{}
	for _, id := range suspended {
		set[id] = true
	}
	svc.SetOrgGuard(orgguard.New(instances, func(context.Context) (map[string]bool, error) { return set, nil }))
}

func TestResumeApp_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	guardFor(t, f.svc, f.app.ProjectID, suspendedTestOrg)

	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "another-developer")
	if !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("ResumeApp err = %v, want ErrOrgSuspended", err)
	}
	if f.status() != apphost.StatusStopped {
		t.Errorf("status = %q, the refused resume must leave the app stopped", f.status())
	}
	if len(f.apps.transitions) != 0 {
		t.Errorf("transitions = %v, want none", f.apps.transitions)
	}
}

func TestResumeAppInBackground_RefusedBeforeItIsAccepted(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	guardFor(t, f.svc, f.app.ProjectID, suspendedTestOrg)
	started := false
	f.svc.async = func(func()) { started = true }

	_, err := f.svc.ResumeAppInBackground(context.Background(), f.app.ProjectID, f.app.ID, "another-developer")
	if !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("err = %v, want ErrOrgSuspended answered at once, not recorded later", err)
	}
	if started {
		t.Error("a refused resume must not be started in the background")
	}
}

func TestResumeApp_APlatformAdminMayResume(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	guardFor(t, f.svc, f.app.ProjectID, suspendedTestOrg)

	ctx := orgguard.WithPlatformOverride(context.Background())
	if _, err := f.svc.ResumeApp(ctx, f.app.ProjectID, f.app.ID, "platform-admin"); err != nil {
		t.Fatalf("ResumeApp as platform admin: %v", err)
	}
	if f.status() != apphost.StatusRunning {
		t.Errorf("status = %q, want running", f.status())
	}
}

func TestResumeApp_AnOrgWithoutASuspensionIsUnaffected(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	guardFor(t, f.svc, f.app.ProjectID, "some-other-org")

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "developer"); err != nil {
		t.Fatalf("ResumeApp: %v", err)
	}
	if f.status() != apphost.StatusRunning {
		t.Errorf("status = %q, want running", f.status())
	}
}

func TestResumeApp_AFailedSuspensionLookupRefuses(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetOrgGuard(orgguard.New(f.svc.instances, func(context.Context) (map[string]bool, error) {
		return nil, errors.New("database down")
	}))
	f.svc.instances.(*fakestore.Instances).Items[f.app.ProjectID].OrgID = suspendedTestOrg

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "developer"); err == nil {
		t.Fatal("want a refusal when the suspension cannot be read")
	}
	if f.status() != apphost.StatusStopped {
		t.Errorf("status = %q, want stopped", f.status())
	}
}

// Every way to start an app's pods is refused, not only the resume route.
func TestDeploys_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	guardFor(t, svc, app.ProjectID, suspendedTestOrg)
	ctx := context.Background()
	origin := apphost.DeployOrigin{Actor: "another-developer"}

	cases := map[string]func() error{
		"deploy": func() error { _, err := svc.DeployAppAs(ctx, app.ProjectID, app.ID, origin); return err },
		"deploy image": func() error {
			_, err := svc.DeployImage(ctx, app.ProjectID, app.ID, "ghcr.io/acme/storefront:2", origin)
			return err
		},
		"deploy current": func() error { _, err := svc.DeployCurrent(ctx, app.ProjectID, app.ID, origin); return err },
		"redeploy (rollback)": func() error {
			_, err := svc.RedeployAppAs(ctx, app.ProjectID, app.ID, "any-deploy", origin)
			return err
		},
		"queued deploy": func() error { _, _, err := svc.QueueDeploy(ctx, app.ProjectID, app.ID, "dev"); return err },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, orgguard.ErrOrgSuspended) {
				t.Fatalf("err = %v, want ErrOrgSuspended", err)
			}
		})
	}
	if len(deploys.deploys) != 0 {
		t.Errorf("recorded %d deploys, want none", len(deploys.deploys))
	}
	if len(kube.Calls) != 0 {
		t.Errorf("cluster calls = %v, want none", kube.Calls)
	}
}

func TestDeployApp_APlatformAdminAndAnUnsuspendedOrgMayDeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	guardFor(t, svc, app.ProjectID, suspendedTestOrg)

	admin := orgguard.WithPlatformOverride(context.Background())
	if _, err := svc.DeployAppAs(admin, app.ProjectID, app.ID, apphost.DeployOrigin{Actor: "platform-admin"}); err != nil {
		t.Fatalf("platform admin deploy: %v", err)
	}

	other := sampleDeployApp()
	svc2, _, _ := newDeployTestService(t, other)
	guardFor(t, svc2, other.ProjectID)
	if _, err := svc2.DeployAppAs(context.Background(), other.ProjectID, other.ID, apphost.DeployOrigin{Actor: "dev"}); err != nil {
		t.Fatalf("unsuspended org deploy: %v", err)
	}
}
