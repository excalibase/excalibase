package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// blockedImage is a made-up reference; nothing ever pulls it.
const blockedImage = "ttl.sh/test:1h"

// registryGuard has the proxy flag off: a blocked registry is refused regardless.
func registryGuard(audit abuse.AuditWriter) *abuse.AppGuard {
	return abuse.NewAppGuard(features.NewStatic(), audit)
}

func TestDeploy_RefusesABlockedRegistryOnEveryDeployPath(t *testing.T) {
	app := sampleDeployApp()
	app.Image = blockedImage
	svc, deploys, kube := newDeployTestService(t, app)
	audit := &proxyAudit{}
	svc.SetProxyGuard(registryGuard(audit))
	ctx := context.Background()

	if _, err := svc.DeployApp(ctx, app.ProjectID, app.ID, "dev-1"); !errors.Is(err, abuse.ErrBlockedRegistry) {
		t.Errorf("DeployApp: %v", err)
	}
	if _, err := svc.DeployAppAs(ctx, app.ProjectID, app.ID, ciOrigin()); !errors.Is(err, abuse.ErrBlockedRegistry) {
		t.Errorf("DeployAppAs: %v", err)
	}
	if _, _, err := svc.QueueDeploy(ctx, app.ProjectID, app.ID, "dev-1"); !errors.Is(err, abuse.ErrBlockedRegistry) {
		t.Errorf("QueueDeploy: %v", err)
	}
	if _, err := svc.DeployCurrent(ctx, app.ProjectID, app.ID, ciOrigin()); !errors.Is(err, abuse.ErrBlockedRegistry) {
		t.Errorf("DeployCurrent: %v", err)
	}
	if countCalls(kube, "ApplyAppWorkload") != 0 || len(deploys.deploys) != 0 {
		t.Errorf("a refused deploy reached the cluster or the store: %v", kube.Calls)
	}
	if len(audit.entries) != 4 || audit.entries[0].Action != abuse.AuditActionRegistryRefused {
		t.Errorf("audit entries = %+v, want 4 registry refusals", audit.entries)
	}
}

func TestDeployImage_RefusesABlockedRegistryBeforeAskingTheRegistry(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.SetProxyGuard(registryGuard(&proxyAudit{}))
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)

	_, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, blockedImage, ciOrigin())

	if !errors.Is(err, abuse.ErrBlockedRegistry) {
		t.Fatalf("err = %v", err)
	}
	if len(resolver.asked) != 0 || countCalls(kube, "ApplyAppWorkload") != 0 {
		t.Errorf("the registry was asked or the cluster touched")
	}
}

// A rollback redeploys an old deploy's stored config, so it is held to the list as it is now.
func TestRedeploy_RefusesABlockedRegistryInTheDeployBeingRolledBackTo(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	waitForDeployStatus(t, deploys, first.ID, apphost.DeployStatusSucceeded)
	deploys.mu.Lock()
	for _, each := range deploys.deploys {
		if each.ID == first.ID {
			each.Config.Image = blockedImage
		}
	}
	deploys.mu.Unlock()
	svc.SetProxyGuard(registryGuard(&proxyAudit{}))
	applied := countCalls(kube, "ApplyAppWorkload")

	if _, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, first.ID, "dev-1"); !errors.Is(err, abuse.ErrBlockedRegistry) {
		t.Fatalf("err = %v", err)
	}
	if countCalls(kube, "ApplyAppWorkload") != applied {
		t.Errorf("a refused rollback reached the cluster: %v", kube.Calls)
	}
}

func resumeMustStayStopped(t *testing.T, f *lifecycleFixture, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
	if slices.Contains(f.kube.Calls, "ResumeAppWorkload:"+f.key()) {
		t.Errorf("a refused resume scaled the app up: %v", f.kube.Calls)
	}
	if f.status() != apphost.StatusStopped {
		t.Errorf("status = %q, want the app left stopped", f.status())
	}
}

func TestResumeApp_RefusesABlockedRegistryInTheRecord(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetProxyGuard(registryGuard(&proxyAudit{}))
	f.app.Image = blockedImage

	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")

	resumeMustStayStopped(t, f, err, abuse.ErrBlockedRegistry)
}

func TestResumeApp_RefusesABlockedRegistryInTheLastDeploy(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetProxyGuard(registryGuard(&proxyAudit{}))
	deploy := &apphost.Deploy{ID: "d1", AppID: f.app.ID, ProjectID: f.app.ProjectID, Status: apphost.DeployStatusSucceeded,
		Config: apphost.DeployConfig{Image: blockedImage, Replicas: 1}}
	if err := f.deploys.Create(deploy); err != nil {
		t.Fatal(err)
	}
	f.deploys.finishSeeded("d1")

	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")

	resumeMustStayStopped(t, f, err, abuse.ErrBlockedRegistry)
}

// The record and the history can both be clean while the paused Deployment holds another image.
func TestResumeApp_RefusesABlockedRegistryInThePausedWorkload(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	audit := &proxyAudit{}
	f.svc.SetProxyGuard(registryGuard(audit))
	f.kube.PausedImages = []string{"ghcr.io/acme/app:1", blockedImage}

	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")

	resumeMustStayStopped(t, f, err, abuse.ErrBlockedRegistry)
	if len(audit.entries) != 1 || audit.entries[0].Action != abuse.AuditActionRegistryRefused {
		t.Errorf("audit entries = %+v, want 1 registry refusal", audit.entries)
	}
}

func TestResumeApp_RefusesWhenThePausedImageCannotBeRead(t *testing.T) {
	cases := map[string]func(f *lifecycleFixture){
		"read fails":  func(f *lifecycleFixture) { f.kube.PausedImagesErr = errors.New("connection refused") },
		"no images":   func(f *lifecycleFixture) { f.kube.PausedImages = []string{} },
		"blank image": func(f *lifecycleFixture) { f.kube.PausedImages = []string{" "} },
	}
	for label, arrange := range cases {
		t.Run(label, func(t *testing.T) {
			f := newLifecycleFixture(t, apphost.StatusStopped)
			f.svc.SetProxyGuard(registryGuard(&proxyAudit{}))
			arrange(f)

			_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")

			if err == nil || slices.Contains(f.kube.Calls, "ResumeAppWorkload:"+f.key()) || f.status() != apphost.StatusStopped {
				t.Fatalf("err = %v, status = %q; want a refusal and the app left stopped", err, f.status())
			}
		})
	}
}

func TestResumeApp_AllowsAnOrdinaryPausedImage(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetProxyGuard(registryGuard(&proxyAudit{}))
	f.kube.PausedImages = []string{"ghcr.io/acme/app:1", "docker.io/ttl.sh-fan/helper:1"}

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatalf("ordinary images refused: %v", err)
	}
}
