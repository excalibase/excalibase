package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

const registryTestPassword = "ghp_never_readable_back"

type memoryVault struct {
	data   map[string]map[string]string
	getErr error
	putErr error
	delErr error
}

func newMemoryVault() *memoryVault { return &memoryVault{data: map[string]map[string]string{}} }

func (m *memoryVault) Get(path string) (map[string]string, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	record, ok := m.data[path]
	if !ok {
		return nil, fmt.Errorf("%w: %s", vault.ErrNotFound, path)
	}
	return record, nil
}

func (m *memoryVault) Put(path string, data map[string]string) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.data[path] = data
	return nil
}

func (m *memoryVault) Delete(path string) error {
	if m.delErr != nil {
		return m.delErr
	}
	delete(m.data, path)
	return nil
}

func (m *memoryVault) DeletePrefix(string) (int, error) { return 0, errors.New("not used") }

func (m *memoryVault) List(prefix string) ([]string, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	out := []string{}
	for path := range m.data {
		if strings.HasPrefix(path, prefix) {
			out = append(out, path)
		}
	}
	return out, nil
}

func (m *memoryVault) Sealed() bool                  { return false }
func (m *memoryVault) GetPublicKey() (string, error) { return "", nil }

func newRegistryService(t *testing.T) (*RegistryCredentialService, *memoryVault, *k8s.MockClient) {
	t.Helper()
	store := newMemoryVault()
	kube := k8s.NewMockClient()
	instances := fakestore.NewInstances()
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", Namespace: "ns-p1"}
	return NewRegistryCredentialService(store, instances, kube), store, kube
}

var testRegistryCredential = apphost.RegistryCredential{Username: "octocat", Password: registryTestPassword}

func TestRegistryCredentials_SetStoresUnderTheNormalizedRegistry(t *testing.T) {
	svc, store, _ := newRegistryService(t)
	registry, err := svc.Set("p1", "Index.Docker.io", testRegistryCredential)
	if err != nil || registry != "docker.io" {
		t.Fatalf("Set = %q, %v", registry, err)
	}
	record := store.data["projects/p1/registries/docker.io"]
	if record["username"] != "octocat" || record["password"] != registryTestPassword {
		t.Fatalf("stored %v", record)
	}
}

func TestRegistryCredentials_SetRefusesBadInput(t *testing.T) {
	svc, store, _ := newRegistryService(t)
	if _, err := svc.Set("p1", "https://ghcr.io", testRegistryCredential); !errors.Is(err, ErrInvalidRegistryCredential) {
		t.Errorf("bad registry: err = %v", err)
	}
	if _, err := svc.Set("p1", "ghcr.io", apphost.RegistryCredential{Username: "u"}); !errors.Is(err, ErrInvalidRegistryCredential) {
		t.Errorf("no password: err = %v", err)
	}
	if len(store.data) != 0 {
		t.Fatalf("something was stored: %v", store.data)
	}
}

func TestRegistryCredentials_ListNamesRegistriesOnly(t *testing.T) {
	svc, store, _ := newRegistryService(t)
	for _, registry := range []string{"ghcr.io", "quay.io"} {
		if _, err := svc.Set("p1", registry, testRegistryCredential); err != nil {
			t.Fatal(err)
		}
	}
	store.data["projects/p2/registries/private.example"] = map[string]string{"username": "x", "password": "y"}

	got, err := svc.List("p1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !slices.Equal(got, []string{"ghcr.io", "quay.io"}) {
		t.Fatalf("List = %v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), registryTestPassword) || strings.Contains(string(raw), "octocat") {
		t.Fatalf("the listing leaked a credential: %s", raw)
	}
}

func TestRegistryCredentials_RemoveRevokesTheRenderedPullSecrets(t *testing.T) {
	svc, store, kube := newRegistryService(t)
	if _, err := svc.Set("p1", "ghcr.io", testRegistryCredential); err != nil {
		t.Fatal(err)
	}
	if err := svc.Remove(context.Background(), "p1", "GHCR.io"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(store.data) != 0 {
		t.Fatalf("still stored: %v", store.data)
	}
	if !slices.Equal(kube.PullSecretsDeleted, []string{"ns-p1/ghcr.io"}) {
		t.Fatalf("pull secrets deleted = %v", kube.PullSecretsDeleted)
	}
}

func TestRegistryCredentials_RemoveFailures(t *testing.T) {
	svc, store, kube := newRegistryService(t)
	store.delErr = errors.New("vault down")
	if err := svc.Remove(context.Background(), "p1", "ghcr.io"); err == nil {
		t.Fatal("a failed vault delete must be reported")
	}
	store.delErr = nil
	kube.PullSecretsDeleteErr = errors.New("api down")
	if err := svc.Remove(context.Background(), "p1", "ghcr.io"); err == nil {
		t.Fatal("a credential still in the cluster must be reported")
	}
	if err := svc.Remove(context.Background(), "p1", "not a host"); !errors.Is(err, ErrInvalidRegistryCredential) {
		t.Fatalf("bad registry: err = %v", err)
	}
}

func TestRegistryCredentials_RemoveWithoutANamespaceOnlyForgets(t *testing.T) {
	svc, _, kube := newRegistryService(t)
	if err := svc.Remove(context.Background(), "p-without-db", "ghcr.io"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(kube.Calls) != 0 {
		t.Fatalf("calls = %v", kube.Calls)
	}
}

func TestRegistryCredentials_Lookup(t *testing.T) {
	svc, store, _ := newRegistryService(t)
	if cred, err := svc.Lookup("p1", "ghcr.io"); err != nil || cred != nil {
		t.Fatalf("none stored: %+v, %v", cred, err)
	}
	if _, err := svc.Set("p1", "ghcr.io", testRegistryCredential); err != nil {
		t.Fatal(err)
	}
	cred, err := svc.Lookup("p1", "ghcr.io")
	if err != nil || cred == nil || *cred != testRegistryCredential {
		t.Fatalf("Lookup = %+v, %v", cred, err)
	}

	store.data["projects/p1/registries/ghcr.io"] = map[string]string{"username": "only"}
	if _, err := svc.Lookup("p1", "ghcr.io"); err == nil {
		t.Fatal("a damaged record must fail the deploy, not pull anonymously")
	}
	store.getErr = errors.New("sealed")
	if _, err := svc.Lookup("p1", "ghcr.io"); err == nil {
		t.Fatal("an unreadable vault must fail the deploy, not pull anonymously")
	}
	if _, err := svc.List("p1"); err == nil {
		t.Fatal("List must report the vault failure")
	}
}

func TestDeployApp_PullsWithTheProjectsCredentialForTheImagesRegistry(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	registries, _, _ := newRegistryService(t)
	if _, err := registries.Set(app.ProjectID, "ghcr.io", testRegistryCredential); err != nil {
		t.Fatal(err)
	}
	svc.SetRegistryCredentials(registries)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	workload := kube.AppWorkloads[testDeployNamespace+"/"+k8s.AppObjectName(app.Name)]
	if workload == nil || workload.PullSecret == nil {
		t.Fatal("the workload was not given a pull secret")
	}

	stored := deploys.getByID(deploy.ID)
	for what, value := range map[string]any{"spec": stored.Spec, "config": stored.Config, "deploy": stored} {
		raw, _ := json.Marshal(value)
		if strings.Contains(string(raw), registryTestPassword) || strings.Contains(string(raw), "octocat") {
			t.Fatalf("the deploy %s holds the registry credential: %s", what, raw)
		}
	}
	raw, _ := json.Marshal(app)
	if strings.Contains(string(raw), registryTestPassword) {
		t.Fatalf("the app row holds the registry credential: %s", raw)
	}
}

func TestDeployApp_AnUnreadableCredentialFailsTheDeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	registries, store, _ := newRegistryService(t)
	store.getErr = errors.New("vault sealed")
	svc.SetRegistryCredentials(registries)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	got := deploys.getByID(deploy.ID)
	if got.Status != apphost.DeployStatusFailed || !strings.Contains(got.FailureReason, "pull credential") {
		t.Fatalf("deploy = %s %q", got.Status, got.FailureReason)
	}
	if len(kube.AppWorkloads) != 0 {
		t.Fatal("nothing may be applied without the credential")
	}
}

func TestDeployApp_NoCredentialMeansAPublicPull(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	registries, _, _ := newRegistryService(t)
	svc.SetRegistryCredentials(registries)

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if workload := kube.AppWorkloads[testDeployNamespace+"/"+k8s.AppObjectName(app.Name)]; workload == nil || workload.PullSecret != nil {
		t.Fatalf("workload = %+v", workload)
	}
}

func TestRegistryCredentials_SetReportsAStoreFailureWithoutDetail(t *testing.T) {
	svc, store, _ := newRegistryService(t)
	store.putErr = errors.New("vault at 10.0.0.9 sealed")
	_, err := svc.Set("p1", "ghcr.io", testRegistryCredential)
	if err == nil || strings.Contains(err.Error(), "10.0.0.9") {
		t.Fatalf("err = %v", err)
	}
}

// A removal that lands between the deploy reading the credential and writing
// its pull secret must not leave the revoked credential in the cluster.
type removingDuringApply struct {
	*k8s.MockClient
	store *memoryVault
}

func (r removingDuringApply) ApplyAppWorkload(ctx context.Context, namespace string, workload *k8s.AppWorkload) error {
	delete(r.store.data, "projects/"+deployTestProject+"/registries/ghcr.io")
	return r.MockClient.ApplyAppWorkload(ctx, namespace, workload)
}

func TestDeployApp_ACredentialRemovedMidDeployIsNotLeftBehind(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	registries, store, _ := newRegistryService(t)
	if _, err := registries.Set(app.ProjectID, "ghcr.io", testRegistryCredential); err != nil {
		t.Fatal(err)
	}
	svc.SetRegistryCredentials(registries)
	svc.runtime = removingDuringApply{MockClient: kube, store: store}

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	got := deploys.getByID(deploy.ID)
	if got.Status != apphost.DeployStatusFailed || !strings.Contains(got.FailureReason, "removed or replaced") {
		t.Fatalf("deploy = %s %q", got.Status, got.FailureReason)
	}
	if !slices.Contains(kube.PullSecretsDeleted, testDeployNamespace+"/ghcr.io") {
		t.Fatalf("the stale pull secret was not deleted: %v", kube.PullSecretsDeleted)
	}
}

func TestDeployApp_AnUnchangedCredentialIsKept(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	registries, _, _ := newRegistryService(t)
	if _, err := registries.Set(app.ProjectID, "ghcr.io", testRegistryCredential); err != nil {
		t.Fatal(err)
	}
	svc.SetRegistryCredentials(registries)
	deploy, _ := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if got := deploys.getByID(deploy.ID); got.Status == apphost.DeployStatusFailed || len(kube.PullSecretsDeleted) != 0 {
		t.Fatalf("deploy = %s, deleted %v", got.Status, kube.PullSecretsDeleted)
	}
}
