package main

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func TestAppNetworkHandlerIsNotMountedWithoutItsStore(t *testing.T) {
	svc := newAppNetworkService(nil, fakestore.NewInstances(), k8s.NewMockClient(), nil)
	if svc != nil || newAppNetworkHandler(svc) != nil {
		t.Error("no platform store must mean no service and no handler")
	}
}

type recordedSweeps struct{ done chan string }

func (r recordedSweeps) EnforceDiskCaps(context.Context)      { r.done <- "disk caps" }
func (r recordedSweeps) SyncAllProjectQuotas(context.Context) { r.done <- "quotas" }

// A plan edit or an org plan change re-sizes every project's quota (EXC-524).
func TestOnPlanChangeResizesQuotasAndDiskCaps(t *testing.T) {
	sweeps := recordedSweeps{done: make(chan string, 2)}
	onPlanChange(sweeps)()
	got := []string{<-sweeps.done, <-sweeps.done}
	if got[0] != "disk caps" || got[1] != "quotas" {
		t.Fatalf("ran %v", got)
	}
}
