package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func TestAppNetworkHandlerIsNotMountedWithoutItsStore(t *testing.T) {
	if newAppNetworkHandler(nil, fakestore.NewInstances(), k8s.NewMockClient(), nil) != nil {
		t.Error("no platform store must mean no handler")
	}
}
