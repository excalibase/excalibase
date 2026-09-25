package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

const (
	envTestOwnerPassword = "owner-pass-4e1"
	envTestSecretValue   = "sk_live_deploy_env_77"
)

// envDeployFixture wires the deploy service to the production resolver over a
// fake vault holding the project's owner login and one app secret.
func envDeployFixture(t *testing.T) (*AppDeployService, *fakeDeployStore, *k8s.MockClient, *fakeVault, *apphost.App) {
	t.Helper()
	app := sampleDeployApp()
	secret := apphost.AppSecretRef(app.ProjectID, app.ID, "API_KEY")
	app.Env = []apphost.EnvVar{
		{Name: "DATABASE_URL", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: "shopdb", Variable: "DATABASE_URL",
		}},
		{Name: "PGHOST", Kind: apphost.KindReference, Reference: &apphost.ReferenceTarget{
			SourceKind: apphost.SourceDatabase, SourceName: "shopdb", Variable: "PGHOST",
		}},
		{Name: "API_KEY", Kind: apphost.KindSecret, Secret: &secret},
	}
	vault := newFakeVault()
	vault.data[vaultCredentialPath(app.ProjectID, roleAdmin)] = map[string]string{
		"host": "db-rw." + testDeployNamespace + ".svc.cluster.local", "port": "5432",
		"database": "shopdb", "username": "shop_owner", "password": envTestOwnerPassword,
	}
	vault.data[secret.Path] = map[string]string{secret.Key: envTestSecretValue}
	instances := fakestore.NewInstances()
	instances.Items[app.ProjectID] = &domain.DatabaseInstance{
		ProjectID: app.ProjectID, Namespace: testDeployNamespace, Status: string(domain.StatusActive),
		DatabaseName: "shopdb", SSLMode: "require",
	}
	deployStore := newFakeDeployStore()
	kube := k8s.NewMockClient()
	svc := NewAppDeployService(newFakeAppStoreForDeploy(app), deployStore, kube, instances,
		NewAppEnvResolver(vault, instances), testDeployRender)
	svc.async = func(f func()) { f() }
	return svc, deployStore, kube, vault, app
}

func TestDeployApp_GivesTheContainerItsDatabaseAndSecretValues(t *testing.T) {
	svc, _, kube, _, app := envDeployFixture(t)
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	if deploy.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("status %q: %s", deploy.Status, deploy.FailureReason)
	}
	workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]
	if workload == nil || workload.EnvSecret == nil {
		t.Fatal("the workload must carry the app's env Secret")
	}
	wantURL := "postgresql://shop_owner:" + envTestOwnerPassword + "@db-rw." + testDeployNamespace +
		".svc.cluster.local:5432/shopdb?sslmode=require"
	if got := string(workload.EnvSecret.Data["DATABASE_URL"]); got != wantURL {
		t.Errorf("DATABASE_URL = %q, want %q", got, wantURL)
	}
	if got := string(workload.EnvSecret.Data["API_KEY"]); got != envTestSecretValue {
		t.Errorf("API_KEY = %q", got)
	}
	annotations := workload.Deployment.Spec.Template.Annotations
	if annotations["excalibase.io/app-env-revision"] != "1" {
		t.Errorf("pods must roll per deploy, annotations = %v", annotations)
	}

	encoded, err := json.Marshal(workload.Deployment)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, value := range []string{envTestOwnerPassword, envTestSecretValue} {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("the Deployment carries a secret value: %s", encoded)
		}
	}
	record, _ := json.Marshal(deploy)
	if strings.Contains(string(record), envTestSecretValue) || strings.Contains(string(record), envTestOwnerPassword) {
		t.Fatalf("the deploy record carries a secret value: %s", record)
	}
}

func TestDeployApp_EachDeployRollsWithItsOwnRevision(t *testing.T) {
	svc, _, kube, _, app := envDeployFixture(t)
	for want := 1; want <= 2; want++ {
		if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1"); err != nil {
			t.Fatalf("DeployApp: %v", err)
		}
		got := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Annotations["excalibase.io/app-env-revision"]
		if got != string(rune('0'+want)) {
			t.Fatalf("deploy %d: env revision %q", want, got)
		}
	}
}

func TestDeployApp_AMissingSecretFailsTheDeployNamingTheVariable(t *testing.T) {
	svc, deployStore, kube, vault, app := envDeployFixture(t)
	delete(vault.data, apphost.AppSecretRef(app.ProjectID, app.ID, "API_KEY").Path)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	stored := waitForDeployStatus(t, deployStore, deploy.ID, apphost.DeployStatusFailed)
	if !strings.Contains(stored.FailureReason, `"API_KEY"`) || !strings.Contains(stored.FailureReason, "no value is stored") {
		t.Errorf("failure reason = %q, want it to name API_KEY and say nothing is stored", stored.FailureReason)
	}
	if _, applied := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]; applied {
		t.Error("nothing may be applied when a value is missing")
	}
}
