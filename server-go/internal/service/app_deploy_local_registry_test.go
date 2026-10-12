package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/imagedigest"
)

// A self-hoster's registry on the host (localhost:5000) is one the platform
// may not dial. When the operator names it, a digest deploy runs that digest
// as given and the registry is never asked (EXC-600).
func TestDeployImage_ADigestOnAnOperatorRegistryDeploysWithoutAskingIt(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	resolver := &stubResolver{err: imagedigest.ErrNotPublic}
	svc.SetImageResolver(resolver)
	svc.SetLocalRegistries([]string{"localhost:5000"})

	image := "localhost:5000/acme/storefront@" + resolvedDigest
	deploy, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, image, ciOrigin())
	if err != nil {
		t.Fatalf("DeployImage: %v", err)
	}
	if len(resolver.asked) != 0 {
		t.Fatalf("an operator registry must never be dialled: %v", resolver.asked)
	}
	if deploy.Image != image || deploy.Digest != resolvedDigest || len(deploys.deploys) != 1 {
		t.Fatalf("deploy = %+v", deploy)
	}
}

func TestDeployImage_AnUnlistedPrivateRegistryIsStillRefused(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	svc.SetImageResolver(&stubResolver{err: imagedigest.ErrNotPublic})
	svc.SetLocalRegistries([]string{"localhost:5000"})

	_, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "10.0.0.5:5000/acme/storefront@"+resolvedDigest, ciOrigin())
	if !errors.Is(err, imagedigest.ErrNotPublic) || len(deploys.deploys) != 0 {
		t.Fatalf("err = %v deploys = %d", err, len(deploys.deploys))
	}
}

// A tag names no fixed build, and resolving it would mean dialling the
// operator's registry, so it is refused with what to do instead.
func TestDeployImage_ATagOnAnOperatorRegistryAsksForADigest(t *testing.T) {
	app := sampleDeployApp()
	svc, deploys, _ := newDeployTestService(t, app)
	resolver := &stubResolver{err: imagedigest.ErrNotPublic}
	svc.SetImageResolver(resolver)
	svc.SetLocalRegistries([]string{"localhost:5000"})

	_, err := svc.DeployImage(context.Background(), app.ProjectID, app.ID, "localhost:5000/acme/storefront:main", ciOrigin())
	if !errors.Is(err, apphost.ErrLocalRegistryNeedsDigest) || len(resolver.asked) != 0 || len(deploys.deploys) != 0 {
		t.Fatalf("err = %v asked = %v", err, resolver.asked)
	}
}
