package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

type fakeAppNetworkSettings struct {
	values map[string]bool
	setErr error
	getErr error
}

func (f *fakeAppNetworkSettings) GetAppPrivateNetwork(_ context.Context, projectID string) (bool, error) {
	return f.values[projectID], f.getErr
}

func (f *fakeAppNetworkSettings) SetAppPrivateNetwork(_ context.Context, projectID string, enabled bool) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.values[projectID] = enabled
	return nil
}

type fakeAppNetworkProjects map[string]*domain.DatabaseInstance

func (f fakeAppNetworkProjects) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	return f[projectID], nil
}

const netTestProject, netTestNamespace = "proj-net1", "org1-proj-net1"

func newAppNetworkFixture(status string) (*AppNetworkService, *fakeAppNetworkSettings, *k8s.MockClient) {
	settings := &fakeAppNetworkSettings{values: map[string]bool{}}
	kube := k8s.NewMockClient()
	projects := fakeAppNetworkProjects{netTestProject: {
		ProjectID: netTestProject, Namespace: netTestNamespace, Status: status, DeploymentMode: domain.ModeK8s,
	}}
	return NewAppNetworkService(settings, projects, kube, nil), settings, kube
}

func TestAppNetwork_OffByDefault(t *testing.T) {
	svc, _, _ := newAppNetworkFixture(string(domain.StatusActive))
	view, err := svc.Describe(context.Background(), netTestProject)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if view.PrivateNetwork || view.Applied {
		t.Fatalf("a new project must be closed: %+v", view)
	}
}

func TestAppNetwork_OnAppliesThePolicyThenRecordsIt(t *testing.T) {
	svc, settings, kube := newAppNetworkFixture(string(domain.StatusActive))
	view, err := svc.Set(context.Background(), netTestProject, true)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !view.PrivateNetwork || !view.Applied {
		t.Fatalf("view = %+v, want on and applied", view)
	}
	if !settings.values[netTestProject] || !kube.AppPrivateNetwork[netTestNamespace] {
		t.Fatalf("setting=%v policy=%v", settings.values[netTestProject], kube.AppPrivateNetwork[netTestNamespace])
	}
}

func TestAppNetwork_OffRemovesThePolicyThenRecordsIt(t *testing.T) {
	svc, settings, kube := newAppNetworkFixture(string(domain.StatusActive))
	ctx := context.Background()
	if _, err := svc.Set(ctx, netTestProject, true); err != nil {
		t.Fatalf("on: %v", err)
	}
	view, err := svc.Set(ctx, netTestProject, false)
	if err != nil {
		t.Fatalf("off: %v", err)
	}
	if view.PrivateNetwork || view.Applied || settings.values[netTestProject] || kube.AppPrivateNetwork[netTestNamespace] {
		t.Fatalf("off must close and record: view=%+v", view)
	}
}

// Nothing is recorded as on unless the cluster took the policy.
func TestAppNetwork_AFailedApplyRecordsNothing(t *testing.T) {
	svc, settings, kube := newAppNetworkFixture(string(domain.StatusActive))
	kube.AppPrivateNetworkErr = errors.New("cilium refused")
	if _, err := svc.Set(context.Background(), netTestProject, true); err == nil {
		t.Fatal("a refused policy must fail the change")
	}
	if settings.values[netTestProject] {
		t.Fatal("the setting was recorded on although the policy was never applied")
	}
}

// A recorded-off project must never be left open: a failed write closes it again.
func TestAppNetwork_AFailedWriteClosesTheNetworkAgain(t *testing.T) {
	svc, settings, kube := newAppNetworkFixture(string(domain.StatusActive))
	settings.setErr = errors.New("db down")
	if _, err := svc.Set(context.Background(), netTestProject, true); err == nil {
		t.Fatal("a failed write must fail the change")
	}
	if kube.AppPrivateNetwork[netTestNamespace] {
		t.Fatal("the policy stayed applied while the setting says off")
	}
}

func TestAppNetwork_TurningOnNeedsAnActiveProject(t *testing.T) {
	svc, _, kube := newAppNetworkFixture(string(domain.StatusPaused))
	if _, err := svc.Set(context.Background(), netTestProject, true); !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("err = %v, want ErrProjectNotActive", err)
	}
	if kube.AppPrivateNetwork[netTestNamespace] {
		t.Fatal("nothing may be applied to an inactive project")
	}
	// Closing is always allowed: it only ever narrows what apps accept.
	if _, err := svc.Set(context.Background(), netTestProject, false); err != nil {
		t.Fatalf("off on a paused project: %v", err)
	}
}

func TestAppNetwork_RefusesUnknownAndNonKubernetesProjects(t *testing.T) {
	svc, _, _ := newAppNetworkFixture(string(domain.StatusActive))
	if _, err := svc.Set(context.Background(), "proj-missing", true); !errors.Is(err, ErrAppNetworkProjectNotFound) {
		t.Fatalf("unknown project: err = %v", err)
	}
	docker := NewAppNetworkService(&fakeAppNetworkSettings{values: map[string]bool{}},
		fakeAppNetworkProjects{netTestProject: {ProjectID: netTestProject, Namespace: netTestNamespace,
			Status: string(domain.StatusActive), DeploymentMode: domain.ModeDocker}}, k8s.NewMockClient(), nil)
	if _, err := docker.Describe(context.Background(), netTestProject); !errors.Is(err, ErrAppNetworkUnsupported) {
		t.Fatalf("docker project: err = %v", err)
	}
}

// The view reports what the cluster holds, not only what was recorded.
func TestAppNetwork_DescribeReportsTheObservedPolicy(t *testing.T) {
	svc, settings, _ := newAppNetworkFixture(string(domain.StatusActive))
	settings.values[netTestProject] = true
	view, err := svc.Describe(context.Background(), netTestProject)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !view.PrivateNetwork || view.Applied {
		t.Fatalf("view = %+v, want on but not applied", view)
	}
}
