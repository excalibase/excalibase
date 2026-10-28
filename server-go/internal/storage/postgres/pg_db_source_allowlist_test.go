//go:build integration

package postgres

import (
	"context"
	"slices"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestDBSourceAllowListDefaultsToUnset(t *testing.T) {
	store := testStore(t)
	got, err := store.GetDBSourceAllowList(context.Background(), "proj-never-chose")
	if err != nil {
		t.Fatalf("GetDBSourceAllowList: %v", err)
	}
	if got.Access != domain.DBSourceAccessUnset || len(got.Sources) != 0 || got.ProjectID != "proj-never-chose" {
		t.Fatalf("got %+v", got)
	}
}

func TestDBSourceAllowListRoundTripsAndReplaces(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	listed := domain.DBSourceAllowList{ProjectID: "proj-a", Access: domain.DBSourceAccessListed, Sources: []string{"198.51.100.7/32", "2001:db8::/32"}}
	if _, err := store.SetDBSourceAllowList(ctx, listed); err != nil {
		t.Fatalf("set listed: %v", err)
	}
	got, err := store.GetDBSourceAllowList(ctx, "proj-a")
	if err != nil || got.Access != domain.DBSourceAccessListed || !slices.Equal(got.Sources, listed.Sources) {
		t.Fatalf("got %+v, %v", got, err)
	}
	saved, err := store.SetDBSourceAllowList(ctx, domain.DBSourceAllowList{ProjectID: "proj-a", Access: domain.DBSourceAccessAny})
	if err != nil || saved.Access != domain.DBSourceAccessAny || len(saved.Sources) != 0 {
		t.Fatalf("replace with any: %+v, %v", saved, err)
	}
	other, err := store.GetDBSourceAllowList(ctx, "proj-b")
	if err != nil || other.Chosen() {
		t.Fatalf("another project is untouched: %+v, %v", other, err)
	}
}

func TestDBSourceAllowListRefusesWhatTheDomainWouldNot(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for name, list := range map[string]domain.DBSourceAllowList{
		"listed with nothing": {ProjectID: "proj-c", Access: domain.DBSourceAccessListed},
		"any with sources":    {ProjectID: "proj-c", Access: domain.DBSourceAccessAny, Sources: []string{"198.51.100.7/32"}},
		"unset":               {ProjectID: "proj-c"},
		"host":                {ProjectID: "proj-c", Access: domain.DBSourceAccessHost},
	} {
		if _, err := store.SetDBSourceAllowList(ctx, list); err == nil {
			t.Errorf("%s: stored", name)
		}
	}
}

func TestDBSourceAllowListDeleteForgetsTheChoice(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if _, err := store.SetDBSourceAllowList(ctx, domain.DBSourceAllowList{ProjectID: "proj-d", Access: domain.DBSourceAccessAny}); err != nil {
		t.Fatalf("set: %v", err)
	}
	for range 2 {
		if err := store.DeleteDBSourceAllowList(ctx, "proj-d"); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	got, err := store.GetDBSourceAllowList(ctx, "proj-d")
	if err != nil || got.Chosen() {
		t.Fatalf("got %+v, %v", got, err)
	}
}
