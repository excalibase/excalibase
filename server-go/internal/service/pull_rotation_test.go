package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/appregistrytest"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

type memSecrets map[string]map[string]string

func (m memSecrets) Get(path string) (map[string]string, error) {
	if record, ok := m[path]; ok {
		return record, nil
	}
	return nil, vault.ErrNotFound
}

func (m memSecrets) Put(path string, data map[string]string) error {
	m[path] = data
	return nil
}

type rotationFixture struct {
	svc       *PullRotationService
	internal  *appregistry.Internal
	kube      *k8s.MockClient
	secrets   memSecrets
	instances *fakestore.Instances
}

func newRotationFixture(t *testing.T, namespace string) rotationFixture {
	t.Helper()
	issuer, err := appregistry.NewIssuer(appregistrytest.KeyPEM(t), appregistrytest.Host)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := appregistry.NewInternal(appregistrytest.Host, nil)
	if err != nil {
		t.Fatal(err)
	}
	secrets := memSecrets{}
	generations := appregistry.NewGenerations(secrets)
	internal := guard.Issuing(issuer, generations)
	instances := fakestore.NewInstances()
	instances.Items["proj-a"] = &domain.DatabaseInstance{ProjectID: "proj-a", Namespace: namespace}
	kube := k8s.NewMockClient()
	return rotationFixture{svc: NewPullRotationService(generations, internal, instances, kube), internal: internal, kube: kube, secrets: secrets, instances: instances}
}

// Rotation re-renders the running apps' pull secrets: they hold the new password, and the old one is refused.
func TestPullRotation_RunningAppsPullWithTheNewCredential(t *testing.T) {
	fx := newRotationFixture(t, "ns-a")
	old, err := fx.internal.PullCredential("proj-a")
	if err != nil {
		t.Fatal(err)
	}
	gen, err := fx.svc.RotatePull(context.Background(), "proj-a")
	if err != nil || gen != 1 || len(fx.kube.PullSecretsRefreshed) != 1 {
		t.Fatalf("rotate: %d %v %v", gen, err, fx.kube.PullSecretsRefreshed)
	}
	written := fx.kube.PullSecretsRefreshed[0]
	if written.Registry != appregistrytest.Host || written.Username != "proj-a" || written.Password == old.Password {
		t.Fatalf("written = %+v", written)
	}
	if ok, _ := fx.internal.VerifyPull("proj-a", written.Password); !ok {
		t.Error("the re-rendered pull secret's password must open the registry")
	}
	if ok, _ := fx.internal.VerifyPull("proj-a", old.Password); ok {
		t.Error("the old password must be refused")
	}
	if !slices.Contains(fx.kube.Calls, "RefreshRegistryPullSecrets:ns-a/"+appregistrytest.Host) {
		t.Errorf("calls = %v", fx.kube.Calls)
	}
}

// A secret left on the old password is never reported as success: the error names the apps.
func TestPullRotation_NamesTheAppsLeftOnTheOldPassword(t *testing.T) {
	fx := newRotationFixture(t, "ns-a")
	fx.kube.PullSecretsRefreshStale, fx.kube.PullSecretsRefreshErr = []string{"api", "web"}, errors.New("conflict")
	gen, err := fx.svc.RotatePull(context.Background(), "proj-a")
	var stale *StalePullSecretsError
	if !errors.As(err, &stale) || gen != 1 || stale.Generation != 1 || !slices.Equal(stale.Apps, []string{"api", "web"}) {
		t.Fatalf("gen %d err %v", gen, err)
	}
	if !strings.Contains(err.Error(), "api, web") {
		t.Errorf("message = %q", err.Error())
	}
}

func TestPullRotation_AProjectWithoutANamespaceOnlyRotates(t *testing.T) {
	fx := newRotationFixture(t, "")
	if gen, err := fx.svc.RotatePull(context.Background(), "proj-a"); err != nil || gen != 1 || len(fx.kube.PullSecretsRefreshed) != 0 {
		t.Fatalf("rotate: %d %v %v", gen, err, fx.kube.PullSecretsRefreshed)
	}
}

// A namespace that cannot be looked up rotates nothing, so no app is left on a retired password unknowingly.
func TestPullRotation_ALookupFailureRotatesNothing(t *testing.T) {
	fx := newRotationFixture(t, "ns-a")
	fx.instances.Err = errors.New("db down")
	if _, err := fx.svc.RotatePull(context.Background(), "proj-a"); err == nil {
		t.Fatal("want the lookup error")
	}
	if len(fx.secrets) != 0 || len(fx.kube.PullSecretsRefreshed) != 0 {
		t.Fatalf("rotated anyway: %v", fx.secrets)
	}
}
