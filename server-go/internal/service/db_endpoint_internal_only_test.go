package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-528: an installation that offers no public database ports still has
// to tell a customer how to reach their database inside the cluster, and
// hand over the CA every TLS login there needs.

func newInternalOnlyFixture(t *testing.T) *mongoEndpointFixture {
	t.Helper()
	f := newMongoEndpointFixture(t, true)
	window, err := domain.NewPortRange(32000, 32099)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	f.svc = NewDBEndpointService(DBEndpointServiceConfig{
		Endpoints:  f.store,
		Instances:  staticInstances{f.inst},
		Kube:       f.kube,
		Ports:      window,
		Quarantine: time.Hour,
	})
	return f
}

func TestWithoutPublicPortsTheInternalEndpointAndCAAreStillDescribed(t *testing.T) {
	f := newInternalOnlyFixture(t)

	view, err := f.svc.Describe(context.Background(), f.inst.ProjectID)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if view.PublicOffered {
		t.Error("an installation without an endpoint domain must not offer public ports")
	}
	if view.Enabled || view.Available || view.Host != "" || view.Port != 0 || view.MongoPort != 0 {
		t.Errorf("no public endpoint may be described: %+v", view)
	}
	if view.Connection != (DBEndpointConnectionStrings{}) {
		t.Errorf("no public connection string may be handed out: %+v", view.Connection)
	}
	if view.Internal.Host != f.inst.Host || view.Internal.Port != postgresPort {
		t.Errorf("internal Postgres endpoint: %+v", view.Internal)
	}
	if view.Internal.MongoHost != k8s.DocumentDBServiceHost(f.inst.ProjectID, f.inst.Namespace) {
		t.Errorf("internal Mongo host: %q", view.Internal.MongoHost)
	}
	if view.CACertificate != "PEM" {
		t.Errorf("CA = %q, want the cluster CA", view.CACertificate)
	}
}

func TestWithPublicPortsConfiguredTheViewSaysTheyAreOffered(t *testing.T) {
	f := newMongoEndpointFixture(t, true)

	view, err := f.svc.Describe(context.Background(), f.inst.ProjectID)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !view.PublicOffered {
		t.Error("an installation with an endpoint domain offers public ports")
	}
}

func TestWithoutPublicPortsOpeningOneIsRefusedAndNothingIsCreated(t *testing.T) {
	f := newInternalOnlyFixture(t)
	ctx := context.Background()

	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); !errors.Is(err, ErrDBEndpointNotConfigured) {
		t.Fatalf("SetPublic(true) error = %v, want ErrDBEndpointNotConfigured", err)
	}
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, false); !errors.Is(err, ErrDBEndpointNotConfigured) {
		t.Fatalf("SetPublic(false) error = %v, want ErrDBEndpointNotConfigured", err)
	}
	if _, err := f.svc.SetRequireTLS(ctx, f.inst.ProjectID, false); !errors.Is(err, ErrDBEndpointNotConfigured) {
		t.Fatalf("SetRequireTLS error = %v, want ErrDBEndpointNotConfigured", err)
	}
	exists, err := f.kube.PublicDBServiceExists(ctx, f.inst.Namespace, domain.DBEndpointServiceName(f.inst.ProjectID))
	if err != nil {
		t.Fatalf("PublicDBServiceExists: %v", err)
	}
	if exists {
		t.Error("a refused open must not leave a Service behind")
	}
}

// Lifecycle steps have no public Service to manage and must not fail a
// pause, resume or delete on such an installation.
func TestWithoutPublicPortsLifecycleStepsAreNoOps(t *testing.T) {
	f := newInternalOnlyFixture(t)
	ctx := context.Background()
	for name, step := range map[string]func(context.Context, *domain.DatabaseInstance) error{
		"withdraw": f.svc.Withdraw, "publish": f.svc.Publish, "release": f.svc.Release,
	} {
		if err := step(ctx, f.inst); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
