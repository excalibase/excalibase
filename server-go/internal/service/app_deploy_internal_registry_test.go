package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/appregistry"
	"github.com/excalibase/provisioning-poc/internal/appregistry/appregistrytest"
)

func testInternalRegistry(t *testing.T) *appregistry.Internal {
	t.Helper()
	return appregistrytest.New(t)
}

func gitBuildOrigin() apphost.DeployOrigin {
	return apphost.DeployOrigin{Actor: "git-build", Source: apphost.DeploySourceGitBuild, CommitSHA: "9fceb02d0ae598e95dc970b74767f19372d61af8"}
}

// A git build's image deploys by digest from the platform's registry, never
// dialled, and pulls with the project's own credential (EXC-540).
func TestDeployImage_AGitBuildDeploysFromTheInternalRegistryWithTheProjectsPullCredential(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	internal := testInternalRegistry(t)
	svc.SetInternalRegistry(internal)

	image, err := internal.Image(app.ProjectID, app.Name, resolvedDigest)
	if err != nil {
		t.Fatal(err)
	}
	deploy, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, image, gitBuildOrigin())
	if err != nil {
		t.Fatalf("DeployImage: %v", err)
	}
	if len(resolver.asked) != 0 || deploy.Digest != resolvedDigest || len(deploys.deploys) != 1 {
		t.Fatalf("asked %v deploy %+v", resolver.asked, deploy)
	}
	var pull *corev1.Secret
	for _, workload := range kube.AppWorkloads {
		pull = workload.PullSecret
	}
	want, _ := internal.PullCredential(app.ProjectID)
	if pull == nil || !strings.Contains(string(pull.Data[corev1.DockerConfigJsonKey]), want.Password) ||
		!strings.Contains(string(pull.Data[corev1.DockerConfigJsonKey]), `"registry.excalibase.internal"`) {
		t.Fatalf("the pull secret does not carry the project's credential: %v", pull)
	}
}

func TestDeployImage_OnlyAGitBuildDeploysFromTheInternalRegistry(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	svc.SetInternalRegistry(testInternalRegistry(t))

	for _, image := range []string{
		"registry.excalibase.internal/" + app.ProjectID + "/storefront@" + resolvedDigest,
		"Registry.Excalibase.Internal/proj-other/storefront@" + resolvedDigest,
	} {
		if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, image, ciOrigin()); !errors.Is(err, appregistry.ErrInternalImage) {
			t.Errorf("%s from the API: %v", image, err)
		}
	}
	tag := "registry.excalibase.internal/" + app.ProjectID + "/storefront:main"
	if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, tag, gitBuildOrigin()); err == nil {
		t.Error("a git build deploys a digest, never a tag")
	}
	other := "registry.excalibase.internal/proj-other/storefront@" + resolvedDigest
	if _, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, other, gitBuildOrigin()); err == nil {
		t.Error("a build deploys only its own project's repository")
	}
	if len(resolver.asked) != 0 || len(deploys.deploys) != 0 {
		t.Fatalf("asked %v deploys %d", resolver.asked, len(deploys.deploys))
	}
}

// A redeploy of a git build's digest pulls with the project's credential, not a saved one.
func TestDeployCurrent_ARedeployOfAGitBuildKeepsTheInternalCredential(t *testing.T) {
	app := sampleDeployApp()
	internal := testInternalRegistry(t)
	app.Image, _ = internal.Image(app.ProjectID, app.Name, resolvedDigest)
	svc, _, kube := newDeployTestService(t, app)
	svc.SetInternalRegistry(internal)
	if _, err := svc.DeployCurrent(context.Background(), app.ProjectID, app.ID, ciOrigin()); err != nil {
		t.Fatalf("DeployCurrent: %v", err)
	}
	want, _ := internal.PullCredential(app.ProjectID)
	for _, workload := range kube.AppWorkloads {
		if workload.PullSecret == nil || !strings.Contains(string(workload.PullSecret.Data[corev1.DockerConfigJsonKey]), want.Password) {
			t.Fatal("the redeploy lost the project's pull credential")
		}
	}
}

// Every rollout path is held to the app's own repository, whatever put the
// image on the app: a record from before the guard, a template, a redeploy.
func TestRollout_AnotherRepositoryOnTheInternalRegistryNeverRuns(t *testing.T) {
	for name, image := range map[string]string{
		"another project":                    appregistrytest.Host + "/proj-other/storefront@" + resolvedDigest,
		"another app":                        appregistrytest.Host + "/" + deployTestProject + "/other@" + resolvedDigest,
		"an alias spelling, another project": appregistrytest.Host + ":443/proj-other/storefront@" + resolvedDigest,
	} {
		app := sampleDeployApp()
		app.Image = image
		svc, _, kube := newDeployTestService(t, app)
		svc.SetInternalRegistry(testInternalRegistry(t))
		deploy, err := svc.DeployAppAs(context.Background(), app.ProjectID, app.ID, ciOrigin())
		if err == nil && (deploy == nil || deploy.Status != apphost.DeployStatusFailed || !strings.Contains(deploy.FailureReason, "own repository")) {
			t.Errorf("%s: deployed %+v", name, deploy)
		}
		for _, workload := range kube.AppWorkloads {
			t.Errorf("%s: a workload was applied: %v", name, workload.Deployment.Name)
		}
	}
}

func TestRegistryCredentials_RefuseTheInternalRegistry(t *testing.T) {
	registries, _, _ := newRegistryService(t)
	registries.SetInternalRegistry(testInternalRegistry(t))
	if _, err := registries.Set(deployTestProject, "Registry.Excalibase.Internal", testRegistryCredential); !errors.Is(err, appregistry.ErrInternalImage) {
		t.Fatalf("a saved credential for the platform's registry: %v", err)
	}
}
