package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

// A plain deploy of a tagged app pins the digest the tag names now, as a CI
// deploy does, so the deploy record says what ran.
func TestDeployCurrent_PinsTheTagsDigest(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	resolver := &stubResolver{digest: resolvedDigest}
	svc.SetImageResolver(resolver)

	deploy, err := svc.DeployCurrent(context.Background(), app.ProjectID, app.ID, ciOrigin())
	if err != nil {
		t.Fatalf("DeployCurrent: %v", err)
	}
	if deploy.Digest != resolvedDigest || deploy.ImageRef != app.Image || deploy.Image != "ghcr.io/acme/storefront@"+resolvedDigest {
		t.Fatalf("deploy = %+v", deploy)
	}
	if len(resolver.asked) != 1 || resolver.asked[0] != app.Image {
		t.Fatalf("asked = %v", resolver.asked)
	}
}

func TestDeployCurrent_ADigestImageRunsThatDigest(t *testing.T) {
	app := sampleDeployApp()
	app.Image = "ghcr.io/acme/storefront@" + resolvedDigest
	svc, _, _ := newDeployTestService(t, app)
	resolver := &stubResolver{digest: "sha256:other"}
	svc.SetImageResolver(resolver)

	deploy, err := svc.DeployCurrent(context.Background(), app.ProjectID, app.ID, ciOrigin())
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Digest != resolvedDigest || deploy.Image != app.Image || len(resolver.asked) != 0 {
		t.Fatalf("deploy = %+v asked = %v", deploy, resolver.asked)
	}
}

// A registry the platform may not dial (a private address) cannot be asked
// for a digest; the node still pulls the tag, so the deploy runs it unpinned.
func TestDeployCurrent_ARegistryOnAPrivateAddressRunsTheTag(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	svc.SetImageResolver(&stubResolver{err: imagedigest.ErrNotPublic})

	deploy, err := svc.DeployCurrent(context.Background(), app.ProjectID, app.ID, ciOrigin())
	if err != nil {
		t.Fatal(err)
	}
	if deploy.Image != app.Image || deploy.Digest != "" {
		t.Fatalf("deploy = %+v", deploy)
	}
}

func TestDeployCurrent_AMissingImageIsRefused(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	svc.SetImageResolver(&stubResolver{err: imagedigest.ErrNotFound})

	if _, err := svc.DeployCurrent(context.Background(), app.ProjectID, app.ID, ciOrigin()); !errors.Is(err, imagedigest.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(deploys.deploys) != 0 {
		t.Fatalf("a refused deploy is not recorded")
	}
}

func TestDeployCurrent_WithoutAResolverRunsTheApp(t *testing.T) {
	app := sampleDeployApp()
	svc, _, _ := newDeployTestService(t, app)
	deploy, err := svc.DeployCurrent(context.Background(), app.ProjectID, app.ID, ciOrigin())
	if err != nil || deploy.Image != app.Image {
		t.Fatalf("deploy = %+v, %v", deploy, err)
	}
}
