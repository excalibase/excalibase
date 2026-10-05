//go:build integration

package apphost_test

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// EXC-543: a deploy remembers who asked for it, the commit it was built from,
// the reference it was named by and the digest that reference resolved to.
func TestPGDeployStore_OriginRoundTrips(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	app := sampleApp("proj_deploy_origin", "app_deploy_origin", "storefront")
	if err := appStore.Create(app, 1); err != nil {
		t.Fatalf("create app: %v", err)
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	deploy := sampleDeploy(app.ProjectID, app.ID, "ci")
	deploy.Image = "ghcr.io/acme/storefront@" + digest
	deploy.Source = apphost.DeploySourceAPI
	deploy.CommitSHA = "9fceb02d0ae598e95dc970b74767f19372d61af8"
	deploy.ImageRef = "ghcr.io/acme/storefront:main"
	deploy.Digest = digest
	if err := deployStore.Create(deploy); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := deployStore.Get(app.ProjectID, app.ID, deploy.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Source != deploy.Source || got.CommitSHA != deploy.CommitSHA || got.ImageRef != deploy.ImageRef || got.Digest != digest {
		t.Fatalf("origin did not round-trip: %+v", got)
	}

	plain := sampleDeploy(app.ProjectID, app.ID, "dev")
	if err := deployStore.Create(plain); err != nil {
		t.Fatalf("create plain: %v", err)
	}
	listed, err := deployStore.ListByApp(app.ProjectID, app.ID, 0)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list: %v %v", listed, err)
	}
	if listed[0].Source != "" || listed[0].CommitSHA != "" || listed[0].Digest != "" || listed[0].ImageRef != "" {
		t.Fatalf("a deploy that named nothing records nothing: %+v", listed[0])
	}
	if listed[1].CommitSHA != deploy.CommitSHA {
		t.Fatalf("listed deploys carry their origin: %+v", listed[1])
	}
}

func TestPGDeployStore_RefusesAnUnknownSource(t *testing.T) {
	appStore := newPGAppStore(t)
	deployStore := apphost.NewPostgresDeployStore(sharedDB)
	app := sampleApp("proj_deploy_badsrc", "app_deploy_badsrc", "storefront")
	if err := appStore.Create(app, 1); err != nil {
		t.Fatalf("create app: %v", err)
	}
	deploy := sampleDeploy(app.ProjectID, app.ID, "ci")
	deploy.Source = "carrier-pigeon"
	if err := deployStore.Create(deploy); err == nil {
		t.Fatal("an unknown source must be refused")
	}
}
