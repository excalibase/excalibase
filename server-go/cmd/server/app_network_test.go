package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

type platformStoreWithAppNetwork struct {
	storage.PlatformStore
	matrixAppNetworkSettings
}

// No fallback: without a place to record the setting, the route is not mounted.
func TestAppNetworkHandlerNeedsItsStoreAndCluster(t *testing.T) {
	instances := fakestore.NewInstances()
	if h := newAppNetworkHandler(nil, instances, k8s.NewMockClient(), nil); h != nil {
		t.Error("no platform store must mean no handler")
	}
	store := platformStoreWithAppNetwork{}
	if h := newAppNetworkHandler(store, instances, nil, nil); h != nil {
		t.Error("no cluster client must mean no handler")
	}
	if h := newAppNetworkHandler(store, instances, k8s.NewMockClient(), nil); h == nil {
		t.Error("a store and a cluster must mount the handler")
	}
}
