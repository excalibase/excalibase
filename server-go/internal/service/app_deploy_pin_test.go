package service

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

// EXC-839: with the flag on a plain deploy runs the digest the tag resolved to,
// and the container never asks the registry again on a restart.
func TestDeployApp_FlagOnPinsTheDigestAndReusesTheNodeCopy(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatalf("DeployApp: %v", err)
	}
	pinned := "ghcr.io/acme/storefront@" + resolvedDigest
	if deploy.Image != pinned || deploy.Config.Image != pinned {
		t.Fatalf("deploy image = %q, config %q, want %q", deploy.Image, deploy.Config.Image, pinned)
	}
	if deploy.ImageRef != "ghcr.io/acme/storefront:1.4.2" || deploy.Digest != resolvedDigest {
		t.Fatalf("the deploy must record what was named and what it resolved to: %+v", deploy)
	}
	container := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0]
	if container.Image != pinned {
		t.Errorf("container image = %q, want %q", container.Image, pinned)
	}
	if container.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("pull policy = %q, want IfNotPresent", container.ImagePullPolicy)
	}
	if len(resolver.asked) != 1 {
		t.Errorf("the registry was asked %d times, want once", len(resolver.asked))
	}
	if got := deploys.getByID(deploy.ID).Status; got != apphost.DeployStatusSucceeded {
		t.Errorf("status = %s", got)
	}
}

// Off, nothing changes: the tag is used as recorded, always pulled, and the registry is never asked.
func TestDeployApp_FlagOffLeavesTheDeployUnchanged(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	svc.SetFeatureFlags(features.NewStatic())

	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	container := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0]
	if container.Image != app.Image || container.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("flag off must keep %q and Always, got %q %q", app.Image, container.Image, container.ImagePullPolicy)
	}
	if len(resolver.asked) != 0 {
		t.Errorf("the registry was asked %d times with the flag off", len(resolver.asked))
	}
}

// A registry that cannot answer fails the deploy with its own words; nothing is recorded or applied, and no tag is run instead.
func TestDeployApp_FlagOnFailsTheDeployWhenTheDigestCannotBeResolved(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, kube := newDeployTestService(t, app)
	svc.SetImageResolver(&stubResolver{err: imagedigest.ErrRateLimited})
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))

	_, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if !errors.Is(err, imagedigest.ErrRateLimited) {
		t.Fatalf("err = %v, want the registry's refusal", err)
	}
	if len(deploys.deploys) != 0 || len(kube.AppWorkloads) != 0 {
		t.Fatal("a deploy that could not be pinned must record and apply nothing")
	}
}

func TestDeployApp_FlagOnWithNoResolverFailsClearly(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); err == nil {
		t.Fatal("want an error: no resolver, no silent tag deploy")
	}
	if len(deploys.deploys) != 0 {
		t.Fatal("nothing may be recorded")
	}
}

// A redeploy (the rollback) runs the digest its source deploy recorded and does not ask the registry.
func TestRedeployApp_FlagOnRunsTheRecordedDigestWithoutAskingTheRegistry(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	resolver.digest = "sha256:" + repeat("cd", 32)

	again, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, first.ID, "dev")
	if err != nil {
		t.Fatalf("RedeployApp: %v", err)
	}
	if again.Config.ImagePull != apphost.ImagePullPublic {
		t.Errorf("rollback mode = %q", again.Config.ImagePull)
	}
	if again.Image != first.Image || again.Digest != first.Digest {
		t.Fatalf("rollback ran %q (%s), want %q (%s)", again.Image, again.Digest, first.Image, first.Digest)
	}
	if len(resolver.asked) != 1 {
		t.Errorf("the registry was asked %d times; the rollback must not ask", len(resolver.asked))
	}
	container := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0]
	if container.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("pull policy = %q", container.ImagePullPolicy)
	}
}

// A rollback to a deploy made before the flag was on has no digest on record:
// it is resolved once now rather than left as a tag that is pulled on every restart.
func TestRedeployApp_FlagOnPinsAnOlderUnpinnedDeploy(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))

	again, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, first.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if again.Digest != resolvedDigest || len(resolver.asked) != 1 {
		t.Fatalf("digest %q, asked %d", again.Digest, len(resolver.asked))
	}
	_ = kube
}

// An image that is already a digest is not resolved again; one anonymous request only decides whether it is public.
func TestDeployApp_FlagOnKeepsAnAlreadyPinnedImage(t *testing.T) {
	app := sampleDeployApp()
	app.Image = "ghcr.io/acme/storefront@" + resolvedDigest
	svc, _, kube := newDeployTestService(t, app)
	resolver := &stubResolver{digest: "sha256:" + repeat("ee", 32)}
	svc.SetImageResolver(resolver)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(resolver.asked) != 1 || resolver.creds[0] != nil || deploy.Image != app.Image || deploy.Digest != resolvedDigest {
		t.Fatalf("asked %d, image %q, digest %q", len(resolver.asked), deploy.Image, deploy.Digest)
	}
	container := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0]
	if container.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("pull policy = %q", container.ImagePullPolicy)
	}
}

// An operator's local registry is never dialled by the platform; its digest image is deployed as given.
func TestDeployApp_FlagOnDoesNotDialAnOperatorRegistry(t *testing.T) {
	app := sampleDeployApp()
	app.Image = "localhost:5000/acme/storefront@" + resolvedDigest
	svc, _, _ := newDeployTestService(t, app)
	svc.SetLocalRegistries([]string{"localhost:5000"})
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	if len(resolver.asked) != 0 {
		t.Fatal("an operator registry must not be dialled")
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// privateRegistry refuses anonymous requests and answers one holding the credential.
type privateRegistry struct {
	asked []*apphost.RegistryCredential
}

func (r *privateRegistry) Resolve(_ context.Context, _ string, cred *apphost.RegistryCredential) (string, error) {
	r.asked = append(r.asked, cred)
	if cred == nil {
		return "", imagedigest.ErrDenied
	}
	return resolvedDigest, nil
}

func privateImageService(t *testing.T) (*AppDeployService, *apphost.App, *privateRegistry, func() corev1.Container) {
	t.Helper()
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	registries, _, _ := newRegistryService(t)
	if _, err := registries.Set(app.ProjectID, "ghcr.io", testRegistryCredential); err != nil {
		t.Fatal(err)
	}
	svc.SetRegistryCredentials(registries)
	registry := &privateRegistry{}
	svc.SetImageResolver(registry)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	container := func() corev1.Container {
		return kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0]
	}
	return svc, app, registry, container
}

// EXC-839: an image that needs the project's credentials is pinned but still
// pulled Always, so a node never hands it to a pod the registry would refuse.
func TestDeployApp_PrivateImageIsPinnedButStillPulledAlways(t *testing.T) {
	svc, app, registry, container := privateImageService(t)

	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	pinned := "ghcr.io/acme/storefront@" + resolvedDigest
	if got := container(); got.Image != pinned || got.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("container = %q %q, want the digest and Always", got.Image, got.ImagePullPolicy)
	}
	if deploy.Config.ImagePull != apphost.ImagePullCredentialed {
		t.Errorf("recorded mode = %q", deploy.Config.ImagePull)
	}
	if len(registry.asked) != 2 || registry.asked[0] != nil || registry.asked[1] == nil {
		t.Errorf("want an anonymous request then one with the credential, got %v", registry.asked)
	}
}

func TestDeployApp_PublicImageRecordsItsMode(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.SetImageResolver(&stubResolver{digest: resolvedDigest})
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Config.ImagePull != apphost.ImagePullPublic {
		t.Errorf("recorded mode = %q", deploy.Config.ImagePull)
	}
}

// A rollback keeps the mode its source recorded, without asking again.
func TestRedeployApp_KeepsTheRecordedPullMode(t *testing.T) {
	svc, app, registry, container := privateImageService(t)
	first, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	asked := len(registry.asked)
	again, err := svc.RedeployApp(context.Background(), app.ProjectID, app.ID, first.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if again.Config.ImagePull != apphost.ImagePullCredentialed || container().ImagePullPolicy != corev1.PullAlways {
		t.Errorf("rollback mode = %q policy %q", again.Config.ImagePull, container().ImagePullPolicy)
	}
	if len(registry.asked) != asked {
		t.Error("a rollback must not ask the registry again")
	}
}

// An image on the platform's own registry needs credentials, so it keeps Always.
func TestDeployApp_AnOperatorRegistryImageKeepsAlways(t *testing.T) {
	app := sampleDeployApp()
	app.Image = "localhost:5000/acme/storefront@" + resolvedDigest
	svc, _, kube := newDeployTestService(t, app)
	svc.SetLocalRegistries([]string{"localhost:5000"})
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	if got := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0].ImagePullPolicy; got != corev1.PullAlways {
		t.Errorf("policy = %q", got)
	}
}

// An already-pinned image whose registry cannot be asked fails the deploy instead of guessing a mode.
func TestDeployApp_FlagOnPinnedImageFailsWhenItsModeCannotBeChecked(t *testing.T) {
	app := sampleDeployApp()
	app.Image = "ghcr.io/acme/storefront@" + resolvedDigest
	svc, deploys, _ := newDeployTestService(t, app)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); err == nil {
		t.Fatal("want an error with no resolver")
	}
	svc.SetImageResolver(&stubResolver{err: imagedigest.ErrUnavailable})
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); !errors.Is(err, imagedigest.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if len(deploys.deploys) != 0 {
		t.Fatal("nothing may be recorded")
	}
}

// A registry that refuses anonymously and no saved credential: the deploy fails with the refusal.
func TestDeployApp_PrivateImageWithoutACredentialFails(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.SetImageResolver(&privateRegistry{})
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	if _, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev"); !errors.Is(err, imagedigest.ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
}

// An already-pinned private image is recognised by the registry's refusal.
func TestDeployApp_PinnedPrivateImageKeepsAlways(t *testing.T) {
	app := sampleDeployApp()
	app.Image = "ghcr.io/acme/storefront@" + resolvedDigest
	svc, _, kube := newDeployTestService(t, app)
	svc.SetImageResolver(&privateRegistry{})
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	deploy, err := svc.DeployApp(context.Background(), app.ProjectID, app.ID, "dev")
	if err != nil {
		t.Fatal(err)
	}
	got := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0]
	if deploy.Config.ImagePull != apphost.ImagePullCredentialed || got.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("mode %q policy %q", deploy.Config.ImagePull, got.ImagePullPolicy)
	}
}

// The platform's own registry isolates tenants by a per-project token, which a
// node's cached copy would skip: its images stay pinned but are pulled Always.
func TestDeployImage_FlagOnKeepsAnInternalRegistryImageOnAlways(t *testing.T) {
	app := sampleDeployApp()
	svc, _, kube := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)
	internal := testInternalRegistry(t)
	svc.SetInternalRegistry(internal)
	svc.SetFeatureFlags(features.NewStatic(features.AppImagePin))
	image, err := internal.Image(app.ProjectID, app.Name, resolvedDigest)
	if err != nil {
		t.Fatal(err)
	}

	deploy, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, image, gitBuildOrigin())
	if err != nil {
		t.Fatal(err)
	}
	got := kube.AppWorkloads[rolloutKey(app, testDeployNamespace)].Deployment.Spec.Template.Spec.Containers[0]
	if got.Image != image || got.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("container = %q %q, want the digest and Always", got.Image, got.ImagePullPolicy)
	}
	if deploy.Config.ImagePull != apphost.ImagePullCredentialed || len(resolver.asked) != 0 {
		t.Errorf("mode %q, registry asked %v", deploy.Config.ImagePull, resolver.asked)
	}
}
