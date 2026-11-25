package adminconsole

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// An app whose deploy or resume is still rolling out holds pods, though its
// status is not yet ACTIVE. A suspension that lists only ACTIVE/FAILED/PAUSING
// apps leaves it to converge and run on.
func TestSuspendStopsAppsStillRollingOut(t *testing.T) {
	svc, store, control, _ := newSuspensionFixture()
	store.owned["victim"] = []OwnedApp{
		{ProjectID: "p1", AppID: "deploying", Name: "deploying", Status: apphost.StatusCreated, Replicas: 3},
		{ProjectID: "p1", AppID: "resuming", Name: "resuming", Status: apphost.StatusResuming, Replicas: 2},
	}

	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "proxy abuse"}); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if len(control.paused) != 2 {
		t.Fatalf("paused %v, want the deploying and the resuming app stopped too", control.paused)
	}
}
