package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

type proxyAudit struct{ entries []*domain.AuditEntry }

func (a *proxyAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

func proxyGuardOn(audit abuse.AuditWriter) *abuse.AppGuard {
	return abuse.NewAppGuard(features.NewStatic(features.ProxyBlock), audit)
}

func proxyEnv(names ...string) []apphost.EnvVar {
	value := "x"
	env := make([]apphost.EnvVar, 0, len(names))
	for _, name := range names {
		env = append(env, apphost.EnvVar{Name: name, Kind: apphost.KindLiteral, Value: &value})
	}
	return env
}

func TestDeploy_RefusesAProxyOnEveryDeployPath(t *testing.T) {
	cases := map[string]func(app *apphost.App){
		"image": func(app *apphost.App) { app.Image = "ghcr.io/xtls/xray-core:1" },
		"name":  func(app *apphost.App) { app.Name = "pxnode" },
		"env":   func(app *apphost.App) { app.Env = proxyEnv("UUID", "WSPATH") },
	}
	for label, mutate := range cases {
		t.Run(label, func(t *testing.T) {
			app := sampleDeployApp()
			mutate(app)
			svc, deploys, kube := newDeployTestService(t, app)
			audit := &proxyAudit{}
			svc.SetProxyGuard(proxyGuardOn(audit))

			if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); !errors.Is(err, abuse.ErrProxyApp) {
				t.Errorf("DeployApp: %v", err)
			}
			if _, err := svc.DeployAppAs(context.Background(), app.ProjectID, app.ID, ciOrigin()); !errors.Is(err, abuse.ErrProxyApp) {
				t.Errorf("DeployAppAs: %v", err)
			}
			if _, _, err := svc.QueueDeploy(context.Background(), app.ProjectID, app.ID, "dev-1"); !errors.Is(err, abuse.ErrProxyApp) {
				t.Errorf("QueueDeploy (templates): %v", err)
			}
			if _, err := svc.DeployCurrent(context.Background(), app.ProjectID, app.ID, ciOrigin()); !errors.Is(err, abuse.ErrProxyApp) {
				t.Errorf("DeployCurrent (image watcher): %v", err)
			}
			if countCalls(kube, "ApplyAppWorkload") != 0 || len(deploys.deploys) != 0 {
				t.Errorf("a refused deploy reached the cluster or the store: %v", kube.Calls)
			}
			if len(audit.entries) != 4 {
				t.Errorf("audit entries = %d, want 4", len(audit.entries))
			}
		})
	}
}

func TestDeployImage_RefusesAProxyImageBeforeAskingTheRegistry(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.SetProxyGuard(proxyGuardOn(&proxyAudit{}))
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)

	_, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "someone/v2ray:latest", ciOrigin())

	if !errors.Is(err, abuse.ErrProxyApp) {
		t.Fatalf("err = %v", err)
	}
	if len(resolver.asked) != 0 || countCalls(kube, "ApplyAppWorkload") != 0 {
		t.Errorf("the registry was asked or the cluster touched")
	}
}

func TestRedeploy_RefusesAProxyConfig(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	waitForDeployStatus(t, deploys, first.ID, apphost.DeployStatusSucceeded)
	deploys.mu.Lock()
	for _, each := range deploys.deploys {
		if each.ID == first.ID {
			each.Config.Image = "teddysun/v2ray"
		}
	}
	deploys.mu.Unlock()
	svc.SetProxyGuard(proxyGuardOn(&proxyAudit{}))

	if _, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, first.ID, "dev-1"); !errors.Is(err, abuse.ErrProxyApp) {
		t.Fatalf("err = %v", err)
	}
}

func TestDeploy_AllowsANormalAppAndAZeroReplicaStop(t *testing.T) {
	app := sampleDeployApp()
	app.Image = "node:22"
	app.Env = proxyEnv("DATABASE_URL", "PORT")
	svc, deploys, _ := newDeployTestService(t, app)
	svc.SetProxyGuard(proxyGuardOn(&proxyAudit{}))

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("normal app refused: %v", err)
	}
	waitForDeployStatus(t, deploys, deploy.ID, apphost.DeployStatusSucceeded)

	stopped := sampleDeployApp()
	stopped.Image = "xray:1"
	stopped.Replicas = 0
	svc2, deploys2, _ := newDeployTestService(t, stopped)
	svc2.SetProxyGuard(proxyGuardOn(&proxyAudit{}))
	stop, err := svc2.DeployApp(context.Background(), stopped.ProjectID, stopped.ID, "dev-1")
	if err != nil {
		t.Fatalf("a zero-replica deploy runs nothing and is allowed: %v", err)
	}
	waitForDeployStatus(t, deploys2, stop.ID, apphost.DeployStatusSucceeded)
}

func TestResumeApp_RechecksTheAppAgainstTheProxyRules(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetProxyGuard(proxyGuardOn(&proxyAudit{}))
	f.app.Image = "teddysun/xray:latest"

	_, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev")

	if !errors.Is(err, abuse.ErrProxyApp) {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(f.kube.Calls, "ResumeAppWorkload:"+f.key()) {
		t.Errorf("a refused resume scaled the app up: %v", f.kube.Calls)
	}
	if f.status() != apphost.StatusStopped {
		t.Errorf("status = %q, want the app left stopped", f.status())
	}
}

// The workload that would come back runs the last deploy's config, which may no longer match the record.
func TestResumeApp_RechecksTheLastDeployedConfig(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetProxyGuard(proxyGuardOn(&proxyAudit{}))
	deploy := &apphost.Deploy{ID: "d1", AppID: f.app.ID, ProjectID: f.app.ProjectID, Status: apphost.DeployStatusSucceeded,
		Config: apphost.DeployConfig{Image: "someone/v2ray", Replicas: 1}}
	if err := f.deploys.Create(deploy); err != nil {
		t.Fatal(err)
	}
	f.deploys.finishSeeded("d1")

	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); !errors.Is(err, abuse.ErrProxyApp) {
		t.Fatalf("err = %v", err)
	}
}

func TestResumeApp_AllowsANormalApp(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetProxyGuard(proxyGuardOn(&proxyAudit{}))
	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); err != nil {
		t.Fatalf("normal app: %v", err)
	}
}
