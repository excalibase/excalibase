//go:build integration

package postgres

import (
	"context"
	"testing"
)

func TestProjectAppNetwork_UnsetProjectIsOff(t *testing.T) {
	store := testStore(t)
	on, err := store.GetAppPrivateNetwork(context.Background(), "proj_net_never_set")
	if err != nil || on {
		t.Fatalf("unset project: on=%v err=%v, want off", on, err)
	}
}

func TestProjectAppNetwork_SetGetRoundTrip(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, want := range []bool{true, false, true} {
		if err := store.SetAppPrivateNetwork(ctx, "proj_net1", want); err != nil {
			t.Fatalf("Set(%v): %v", want, err)
		}
		got, err := store.GetAppPrivateNetwork(ctx, "proj_net1")
		if err != nil || got != want {
			t.Fatalf("Get after Set(%v): %v %v", want, got, err)
		}
	}
}
