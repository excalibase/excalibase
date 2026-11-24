package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func suspendTemplateOrg(rig *tplRig, orgs ...string) {
	set := map[string]bool{}
	for _, id := range orgs {
		set[id] = true
	}
	instances := fakestore.NewInstances()
	instances.Items[tplProject] = rig.project
	rig.svc.deps.Guard = orgguard.New(instances, func(context.Context) (map[string]bool, error) { return set, nil })
}

func TestDeployTemplate_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	rig := newTemplateRig(t)
	suspendTemplateOrg(rig, "org1")

	_, err := rig.svc.Deploy(context.Background(), tplProject, "web-redis", "dev", adminConfirmed)
	if !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("err = %v, want ErrOrgSuspended", err)
	}
	rig.expectNothingDone(t)
}

func TestDeployTemplate_APlatformAdminAndAnUnsuspendedOrgMayDeploy(t *testing.T) {
	rig := newTemplateRig(t)
	suspendTemplateOrg(rig, "org1")
	if _, err := rig.svc.Deploy(orgguard.WithPlatformOverride(context.Background()), tplProject, "redis", "admin", adminConfirmed); err != nil {
		t.Fatalf("platform admin: %v", err)
	}

	other := newTemplateRig(t)
	suspendTemplateOrg(other, "another-org")
	if _, err := other.svc.Deploy(context.Background(), tplProject, "redis", "dev", adminConfirmed); err != nil {
		t.Fatalf("unsuspended org: %v", err)
	}
}
