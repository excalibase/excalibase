package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

var resolvedDigest = "sha256:" + strings.Repeat("ab", 32)

// stubResolver answers every image with one digest and records what it was asked.
type stubResolver struct {
	mu     sync.Mutex
	digest string
	err    error
	asked  []string
	creds  []*apphost.RegistryCredential
}

func (r *stubResolver) Resolve(_ context.Context, image string, cred *apphost.RegistryCredential) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, image)
	r.creds = append(r.creds, cred)
	if r.err != nil {
		return "", r.err
	}
	return r.digest, nil
}

func ciOrigin() apphost.DeployOrigin {
	return apphost.DeployOrigin{Actor: "ci-user", Source: apphost.DeploySourceAPI, CommitSHA: "9fceb02d0ae598e95dc970b74767f19372d61af8"}
}

func TestDeployImage_RunsTheDigestTheTagResolvedTo(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)

	deploy, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "ghcr.io/acme/storefront:main", ciOrigin())
	if err != nil {
		t.Fatalf("DeployImage: %v", err)
	}
	pinned := "ghcr.io/acme/storefront@" + resolvedDigest
	if deploy.Image != pinned || deploy.Config.Image != pinned || deploy.Spec.Image != pinned {
		t.Fatalf("the deploy must run the pinned digest: image %q config %q spec %q", deploy.Image, deploy.Config.Image, deploy.Spec.Image)
	}
	if deploy.ImageRef != "ghcr.io/acme/storefront:main" || deploy.Digest != resolvedDigest {
		t.Fatalf("the deploy records what was named and what it resolved to: %+v", deploy)
	}
	if deploy.Source != apphost.DeploySourceAPI || deploy.CommitSHA != ciOrigin().CommitSHA || deploy.CreatedBy != "ci-user" {
		t.Fatalf("origin = %q %q %q", deploy.Source, deploy.CommitSHA, deploy.CreatedBy)
	}
	if stored := deploys.getByID(deploy.ID); stored.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("status = %s (%s)", stored.Status, stored.FailureReason)
	}
	workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]
	if got := workload.Deployment.Spec.Template.Spec.Containers[0].Image; got != pinned {
		t.Fatalf("the container runs %q, want %q", got, pinned)
	}
	stored, _ := svc.apps.Get(app.ProjectID, app.ID)
	if stored.Image != "ghcr.io/acme/storefront:main" || stored.ResolvedDigest != resolvedDigest {
		t.Fatalf("the app records the reference and the digest it resolved to: %q %q", stored.Image, stored.ResolvedDigest)
	}
	if len(deploys.deploys) != 1 {
		t.Fatalf("deploys = %d", len(deploys.deploys))
	}
}

func TestDeployImage_AsksTheRegistryWithTheProjectsCredential(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	registries, _, _ := newRegistryService(t)
	if _, err := registries.Set(app.ProjectID, "ghcr.io", testRegistryCredential); err != nil {
		t.Fatal(err)
	}
	svc.SetRegistryCredentials(registries)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)

	if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "ghcr.io/acme/storefront:main", ciOrigin()); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "acme/storefront:main", ciOrigin()); err != nil {
		t.Fatal(err)
	}
	if resolver.creds[0] == nil || resolver.creds[0].Password != registryTestPassword {
		t.Fatalf("ghcr.io was asked without its saved credential: %+v", resolver.creds[0])
	}
	if resolver.creds[1] != nil {
		t.Fatal("a credential saved for ghcr.io must never be sent to Docker Hub")
	}
}

func TestDeployImage_ARegistryRefusalCreatesNoDeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetImageResolver(&stubResolver{err: imagedigest.ErrNotFound})

	_, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "ghcr.io/acme/storefront:nope", ciOrigin())
	if !errors.Is(err, imagedigest.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(deploys.deploys) != 0 || len(kube.AppWorkloads) != 0 {
		t.Fatal("nothing may be recorded or applied for an image the registry does not have")
	}
	if stored, _ := svc.apps.Get(app.ProjectID, app.ID); stored.Image != app.Image {
		t.Fatalf("the app must keep its image: %q", stored.Image)
	}
}

func TestDeployImage_RefusesBeforeAskingTheRegistry(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)

	if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "nginx", ciOrigin()); !errors.Is(err, apphost.ErrInvalidImage) {
		t.Fatalf("an image with no tag: %v", err)
	}
	if _, err := svc.DeployImage(context.Background(), app.ProjectID, "app_missing", "nginx:1", ciOrigin()); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("an unknown app: %v", err)
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("the registry was asked about %v", resolver.asked)
	}
}

func TestDeployImage_AnUnreadableCredentialRefuses(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	registries, store, _ := newRegistryService(t)
	store.getErr = errors.New("vault sealed")
	svc.SetRegistryCredentials(registries)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)

	if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "ghcr.io/acme/storefront:main", ciOrigin()); err == nil {
		t.Fatal("a credential that cannot be read must not become an anonymous request")
	}
	if len(resolver.asked) != 0 || len(deploys.deploys) != 0 {
		t.Fatal("nothing may be asked or recorded")
	}
}

func TestDeployImage_WithoutAResolverRefuses(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "ghcr.io/acme/storefront:main", ciOrigin()); err == nil {
		t.Fatal("an image cannot be pinned without a resolver")
	}
}

func TestDeployAppAs_RecordsTheOrigin(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	origin := apphost.DeployOrigin{Actor: "dev-1", Source: apphost.DeploySourceStudio}

	deploy, err := svc.DeployAppAs(context.Background(), app.ProjectID, app.ID, origin)
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Source != apphost.DeploySourceStudio || deploy.CreatedBy != "dev-1" || deploy.Image != app.Image || deploy.Digest != "" {
		t.Fatalf("deploy = %+v", deploy)
	}
}

// Rolling back runs the older deploy's digest again and keeps the commit it was built from.
func TestRedeployAppAs_RollsBackToTheExactDigest(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	svc.SetImageResolver(&stubResolver{digest: resolvedDigest})
	first, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "ghcr.io/acme/storefront:main", ciOrigin())
	if err != nil {
		t.Fatal(err)
	}
	newer := "sha256:" + strings.Repeat("cd", 32)
	svc.SetImageResolver(&stubResolver{digest: newer})
	if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "ghcr.io/acme/storefront:main", apphost.DeployOrigin{Actor: "ci-user", Source: apphost.DeploySourceAPI}); err != nil {
		t.Fatal(err)
	}

	rollback, err := svc.RedeployAppAs(context.Background(), app.ProjectID, app.ID, first.ID,
		apphost.DeployOrigin{Actor: "dev-2", Source: apphost.DeploySourceStudio})
	if err != nil {
		t.Fatal(err)
	}
	if rollback.Image != first.Image || rollback.Digest != resolvedDigest || rollback.ImageRef != first.ImageRef ||
		rollback.CommitSHA != first.CommitSHA || rollback.RedeployOf != first.ID || rollback.Source != apphost.DeploySourceStudio {
		t.Fatalf("rollback = %+v", rollback)
	}
	workload := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)]
	if got := workload.Deployment.Spec.Template.Spec.Containers[0].Image; got != first.Image {
		t.Fatalf("the container runs %q after the rollback, want %q", got, first.Image)
	}
}

func TestGetDeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetDeploy(app.ProjectID, app.ID, deploy.ID)
	if err != nil || got.ID != deploy.ID || got.Status != apphost.DeployStatusSucceeded {
		t.Fatalf("GetDeploy = %+v, %v", got, err)
	}
	if _, err := svc.GetDeploy(app.ProjectID, app.ID, "nope"); !errors.Is(err, apphost.ErrDeployNotFound) {
		t.Fatalf("unknown deploy: %v", err)
	}
	if _, err := svc.GetDeploy(app.ProjectID, "app_other", deploy.ID); !errors.Is(err, apphost.ErrAppNotFound) {
		t.Fatalf("another app: %v", err)
	}
}
