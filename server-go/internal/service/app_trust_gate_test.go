package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

// EXC-899: a limited owner's account cannot start a container by any path.

type fixedTrust struct {
	state trust.State
	err   error
}

func (f fixedTrust) ForProject(context.Context, string) (trust.State, error) { return f.state, f.err }

func trustGate(state trust.State) *trust.Gate {
	return trust.NewGate(fixedTrust{state: state}, trust.Policy{MinAgeDays: 90, MinPublicEvents: 5, ActivityWindowDays: 90, MinRepos: 3, RepoMinAgeDays: 30})
}

func TestContainerStarts_RefusedForALimitedOwner(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetContainerGate(trustGate(trust.Limited))
	ctx := context.Background()
	origin := apphost.DeployOrigin{Actor: "dev"}

	cases := map[string]func() error{
		"deploy": func() error { _, err := svc.DeployAppAs(ctx, app.ProjectID, app.ID, origin); return err },
		"deploy image": func() error {
			_, err := svc.DeployImage(ctx, app.ProjectID, app.ID, "ghcr.io/acme/storefront:2", origin)
			return err
		},
		"deploy current": func() error { _, err := svc.DeployCurrent(ctx, app.ProjectID, app.ID, origin); return err },
		"redeploy":       func() error { _, err := svc.RedeployAppAs(ctx, app.ProjectID, app.ID, "any", origin); return err },
		"queued deploy":  func() error { _, _, err := svc.QueueDeploy(ctx, app.ProjectID, app.ID, "dev"); return err },
		"image watcher": func() error {
			_, err := svc.DeployAppAs(ctx, app.ProjectID, app.ID, apphost.DeployOrigin{Actor: ImageWatcherActor, Source: apphost.DeploySourceImageWatcher})
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, trust.ErrVerificationRequired) {
				t.Fatalf("err = %v, want ErrVerificationRequired", err)
			}
		})
	}
	if len(deploys.deploys) != 0 || len(kube.Calls) != 0 {
		t.Errorf("a refused deploy still recorded %d deploys and made cluster calls %v", len(deploys.deploys), kube.Calls)
	}
}

func TestResume_RefusedForALimitedOwnerAndPauseStaysOpen(t *testing.T) {
	f := newLifecycleFixture(t, apphost.StatusStopped)
	f.svc.SetContainerGate(trustGate(trust.Limited))
	if _, err := f.svc.ResumeApp(context.Background(), f.app.ProjectID, f.app.ID, "dev"); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("ResumeApp err = %v, want ErrVerificationRequired", err)
	}
	started := false
	f.svc.async = func(func()) { started = true }
	if _, err := f.svc.ResumeAppInBackground(context.Background(), f.app.ProjectID, f.app.ID, "dev"); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("ResumeAppInBackground err = %v, want it refused at once", err)
	}
	if started || f.status() != apphost.StatusStopped || len(f.apps.transitions) != 0 {
		t.Errorf("a refused resume changed something: started=%v status=%s transitions=%v", started, f.status(), f.apps.transitions)
	}

	running := newLifecycleFixture(t, apphost.StatusRunning)
	running.svc.SetContainerGate(trustGate(trust.Limited))
	if _, err := running.svc.PauseApp(context.Background(), running.app.ProjectID, running.app.ID); err != nil {
		t.Fatalf("a limited owner could not stop its own app: %v", err)
	}
}

func TestContainerStarts_AVerifiedOwnerAndAnUnreadableCheck(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.SetContainerGate(trustGate(trust.Full))
	if _, err := svc.DeployAppAs(context.Background(), app.ProjectID, app.ID, apphost.DeployOrigin{Actor: "dev"}); err != nil {
		t.Fatalf("a verified owner was refused: %v", err)
	}

	unreadable := sampleDeployApp()
	svc2, deploys, _ := newDeployTestService(t, unreadable)
	svc2.SetContainerGate(trust.NewGate(fixedTrust{state: trust.Limited, err: errors.New("db down")}, trust.Policy{}))
	if _, err := svc2.DeployAppAs(context.Background(), unreadable.ProjectID, unreadable.ID, apphost.DeployOrigin{Actor: "dev"}); !errors.Is(err, trust.ErrCheckUnavailable) {
		t.Fatalf("err = %v, want ErrCheckUnavailable (fail closed)", err)
	}
	if len(deploys.deploys) != 0 {
		t.Error("an unreadable check still let a deploy through")
	}
}

func TestDeployTemplate_RefusedForALimitedOwner(t *testing.T) {
	rig := newTemplateRig(t)
	rig.svc.deps.ContainerGate = trustGate(trust.Limited)
	if _, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed); !errors.Is(err, trust.ErrVerificationRequired) {
		t.Fatalf("err = %v, want ErrVerificationRequired", err)
	}
	rig.expectNothingDone(t)

	ok := newTemplateRig(t)
	ok.svc.deps.ContainerGate = trustGate(trust.Full)
	if _, err := ok.svc.Deploy(context.Background(), tplProject, "redis", "dev", adminConfirmed); err != nil {
		t.Fatalf("a verified owner was refused: %v", err)
	}
}
