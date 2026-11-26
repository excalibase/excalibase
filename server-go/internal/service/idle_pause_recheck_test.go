package service

import (
	"context"
	"log"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// other is the second project of the pair: the one not named.
func other(project string) string {
	if project == "first" {
		return "second"
	}
	return "first"
}

// EXC-894: the sweep lists every idle project, then pauses them one by one,
// each pause taking minutes. A customer who comes back in between must not be
// paused on the strength of the listing.
func TestIdlePause_AProjectSeenSinceTheListingIsNotPaused(t *testing.T) {
	f := newIdleFixture(t)
	var logs strings.Builder
	f.scheduler.logger = log.New(&logs, "", 0)
	f.project(t, "first", domain.Free, 8*day)
	f.project(t, "second", domain.Free, 8*day)
	f.pauser.afterPause = func(paused string) {
		_ = f.activity.TouchProjectActivity(context.Background(), other(paused), "api", f.clock.Now())
	}

	report := f.run(t)

	if len(report.Paused) != 1 {
		t.Fatalf("only the project nobody came back to is paused: %+v", report)
	}
	if len(report.Failed) != 0 {
		t.Fatalf("a project that is no longer idle is not a failure: %+v", report)
	}
	if len(report.Skipped) != 1 || report.Skipped[0] != other(report.Paused[0]) {
		t.Fatalf("the stand-down is reported: %+v", report)
	}
	if got := logs.String(); !strings.Contains(got, other(report.Paused[0])+" is no longer idle") {
		t.Fatalf("the stand-down is logged with the project id: %q", got)
	}
	if waiting, _ := f.instances.FindByProjectID(other(report.Paused[0])); waiting.Status != "ACTIVE" {
		t.Fatalf("%s is %s, want ACTIVE", waiting.ProjectID, waiting.Status)
	}
	if got := f.audit.actions(); len(got) != 1 {
		t.Fatalf("a pause that did not happen is not audited: %v", got)
	}
}

func TestIdlePause_AProjectResumedSinceTheListingIsNotPaused(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "first", domain.Free, 8*day)
	f.project(t, "second", domain.Free, 8*day)
	f.pauser.afterPause = func(paused string) {
		waiting, _ := f.instances.FindByProjectID(other(paused))
		waiting.LastActiveAt = &domain.FlexTime{Time: f.clock.Now()}
		_ = f.instances.Update(waiting)
	}

	report := f.run(t)

	if len(report.Paused) != 1 {
		t.Fatalf("a project resumed since the listing is left alone: %+v", report)
	}
}

func TestIdlePause_AProjectMovedOffThePlanSinceTheListingIsNotPaused(t *testing.T) {
	f := newIdleFixture(t)
	f.project(t, "first", domain.Free, 8*day)
	f.project(t, "second", domain.Free, 8*day)
	f.pauser.afterPause = func(paused string) {
		waiting, _ := f.instances.FindByProjectID(other(paused))
		waiting.Tier = domain.Standard
		_ = f.instances.Update(waiting)
	}

	report := f.run(t)

	if len(report.Paused) != 1 {
		t.Fatalf("a plan with no auto-pause is left alone: %+v", report)
	}
}
