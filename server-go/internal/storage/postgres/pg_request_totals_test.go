//go:build integration

package postgres

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

func totalsByFamily(t *testing.T, store *Store, projectID string) map[string]domain.RequestTotalsRow {
	t.Helper()
	rows, err := store.RequestTotals(context.Background(), projectID)
	if err != nil {
		t.Fatalf("RequestTotals: %v", err)
	}
	out := map[string]domain.RequestTotalsRow{}
	for _, row := range rows {
		if row.ProjectID != projectID {
			t.Errorf("another project's row: %+v", row)
		}
		out[row.Family] = row
	}
	return out
}

// EXC-657: running totals only grow, so they export as counters.
func TestRequestTotals_AddAcrossSamples(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	first := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	if err := store.AddRequestTotals(ctx, []domain.RequestMetricsRow{
		{ProjectID: "p1", Timestamp: first, Family: "graphql", Requests: 10, ClientErrors: 2, ServerErrors: 1,
			LatencyBuckets: []int64{5, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}},
		{ProjectID: "p1", Timestamp: first, Family: "realtime", Connections: 3, ConnectionsOpened: 1, Messages: 40},
		{ProjectID: "p2", Timestamp: first, Family: "graphql", Requests: 99},
	}); err != nil {
		t.Fatalf("AddRequestTotals: %v", err)
	}
	second := first.Add(time.Minute)
	if err := store.AddRequestTotals(ctx, []domain.RequestMetricsRow{
		{ProjectID: "p1", Timestamp: second, Family: "graphql", Requests: 4, ServerErrors: 1,
			LatencyBuckets: []int64{0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
		{ProjectID: "p1", Timestamp: second, Family: "realtime", Connections: 1, Messages: 2},
	}); err != nil {
		t.Fatalf("AddRequestTotals: %v", err)
	}

	got := totalsByFamily(t, store, "p1")
	graphql := got["graphql"]
	if graphql.Requests != 14 || graphql.ClientErrors != 2 || graphql.ServerErrors != 2 {
		t.Errorf("graphql counts: %+v", graphql)
	}
	if want := []int64{5, 7, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}; !reflect.DeepEqual(graphql.LatencyBuckets, want) {
		t.Errorf("buckets add per bucket: %v", graphql.LatencyBuckets)
	}
	realtime := got["realtime"]
	if realtime.Connections != 1 || realtime.ConnectionsOpened != 1 || realtime.Messages != 42 {
		t.Errorf("realtime: open is the latest reading, the rest add: %+v", realtime)
	}
	if !realtime.UpdatedAt.Equal(second) {
		t.Errorf("updated at %v", realtime.UpdatedAt)
	}
	if len(totalsByFamily(t, store, "p2")) != 1 {
		t.Error("p2 keeps its own totals")
	}
}

func TestRequestTotals_BucketsOfDifferentLengthsStillAdd(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	if err := store.AddRequestTotals(ctx, []domain.RequestMetricsRow{{ProjectID: "p1", Timestamp: at, Family: "auth", Requests: 1}}); err != nil {
		t.Fatalf("AddRequestTotals: %v", err)
	}
	if err := store.AddRequestTotals(ctx, []domain.RequestMetricsRow{{ProjectID: "p1", Timestamp: at, Family: "auth", Requests: 2,
		LatencyBuckets: []int64{1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}}}); err != nil {
		t.Fatalf("AddRequestTotals: %v", err)
	}
	if got := totalsByFamily(t, store, "p1")["auth"]; got.Requests != 3 || len(got.LatencyBuckets) != 12 || got.LatencyBuckets[0] != 1 {
		t.Errorf("auth: %+v", got)
	}
}

func TestRequestTotals_AnotherBucketLayoutRestartsTheHistogram(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	for _, buckets := range [][]int64{{1, 2, 3}, {5, 0, 0, 0}} {
		if err := store.AddRequestTotals(ctx, []domain.RequestMetricsRow{{ProjectID: "p1", Timestamp: at, Family: "rest", Requests: 1, LatencyBuckets: buckets}}); err != nil {
			t.Fatalf("AddRequestTotals: %v", err)
		}
	}
	if got := totalsByFamily(t, store, "p1")["rest"]; !reflect.DeepEqual(got.LatencyBuckets, []int64{5, 0, 0, 0}) || got.Requests != 2 {
		t.Errorf("rest: %+v", got)
	}
}

func TestRequestTotals_PruneDropsIdleTotals(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	if err := store.AddRequestTotals(ctx, []domain.RequestMetricsRow{
		{ProjectID: "p1", Timestamp: at.Add(-8 * 24 * time.Hour), Family: "rest", Requests: 1},
		{ProjectID: "p1", Timestamp: at, Family: "graphql", Requests: 1},
	}); err != nil {
		t.Fatalf("AddRequestTotals: %v", err)
	}
	if err := store.PruneRequestTotalsBefore(ctx, at.Add(-7*24*time.Hour)); err != nil {
		t.Fatalf("PruneRequestTotalsBefore: %v", err)
	}
	got := totalsByFamily(t, store, "p1")
	if _, kept := got["rest"]; kept || len(got) != 1 {
		t.Errorf("only the idle total is dropped: %+v", got)
	}
}

func TestListTokensByProject_OnlyThatProjectsBoundTokens(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	for _, id := range []string{"u1", "u2"} {
		if err := store.CreateUser(ctx, &domain.User{ID: id, Username: id, Email: id + "@e.com",
			PasswordHash: testutil.FixturePasswordHash(), Role: "user", Active: true}); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
	}
	for _, token := range []*domain.AccessToken{
		{TokenHash: "h1", TokenPrefix: "excb_1", UserID: "u1", Name: "grafana", Scopes: "metrics:read", ProjectID: "p1"},
		{TokenHash: "h2", TokenPrefix: "excb_2", UserID: "u2", Name: "ci", Scopes: "read", ProjectID: "p1"},
		{TokenHash: "h3", TokenPrefix: "excb_3", UserID: "u1", Name: "other", Scopes: "metrics:read", ProjectID: "p2"},
		{TokenHash: "h4", TokenPrefix: "excb_4", UserID: "u1", Name: "unbound"},
	} {
		if err := store.CreateToken(ctx, token); err != nil {
			t.Fatalf("CreateToken: %v", err)
		}
	}
	tokens, err := store.ListTokensByProject(ctx, "p1")
	if err != nil {
		t.Fatalf("ListTokensByProject: %v", err)
	}
	hashes := map[string]bool{}
	for _, token := range tokens {
		hashes[token.TokenHash] = true
	}
	if len(tokens) != 2 || !hashes["h1"] || !hashes["h2"] {
		t.Errorf("p1's tokens: %v", hashes)
	}
}
