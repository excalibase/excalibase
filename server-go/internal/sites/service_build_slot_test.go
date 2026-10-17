package sites_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// A git build counts toward the project's deploys per hour when it is queued
// (EXC-354), and the deploy it makes is not counted a second time.
func TestBuildsShareTheHourlyDeployLimit(t *testing.T) {
	limits := sites.DefaultLimits
	limits.MaxDeploysPerHour = 2
	h := newLimitsHarness(t, fixedLimits{limits})
	site, err := h.service.Create(context.Background(), testProject, "shop", nil, testActor)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := h.service.TakeDeploySlot(context.Background(), testProject); err != nil {
		t.Fatalf("a queued build takes a slot: %v", err)
	}
	built, err := h.service.Deploy(context.Background(), sites.DeployRequest{ProjectID: testProject, SiteID: site.ID, Actor: testActor,
		Body: bytes.NewReader(viteDist(t)), SlotTaken: true})
	if err != nil {
		t.Fatalf("the build's own deploy: %v", err)
	}
	if built.Status != sites.DeploySucceeded {
		t.Fatalf("deploy %+v", built)
	}
	if _, err := h.deploy(t, site); err != nil {
		t.Fatalf("one upload still fits the hour: %v", err)
	}
	if err := h.service.TakeDeploySlot(context.Background(), testProject); !errors.Is(err, sites.ErrDeployRateLimited) {
		t.Fatalf("a third start this hour: err = %v, want the hourly rate", err)
	}
}
