//go:build integration

package postgres

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestRequestMetrics_AppendReadAndPrune(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	rows := []domain.RequestMetricsRow{
		{ProjectID: "p1", Timestamp: at, Family: "graphql", Requests: 10, ClientErrors: 2, ServerErrors: 1,
			LatencyBuckets: []int64{5, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}},
		{ProjectID: "p1", Timestamp: at, Family: "realtime", Connections: 3, ConnectionsOpened: 1, Messages: 40},
		{ProjectID: "p2", Timestamp: at, Family: "auth", Requests: 1, LatencyBuckets: []int64{1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}},
		{ProjectID: "p1", Timestamp: at.Add(-8 * 24 * time.Hour), Family: "rest", Requests: 4},
	}
	if err := store.AppendRequestMetrics(ctx, rows); err != nil {
		t.Fatalf("AppendRequestMetrics: %v", err)
	}

	got, err := store.RequestMetricsSince(ctx, "p1", at.Add(-time.Hour))
	if err != nil {
		t.Fatalf("RequestMetricsSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("only p1's rows in range: %+v", got)
	}
	for i := range got {
		got[i].Timestamp = got[i].Timestamp.UTC()
	}
	if !reflect.DeepEqual(got[0], rows[0]) && !reflect.DeepEqual(got[1], rows[0]) {
		t.Errorf("graphql row round-trips: %+v", got)
	}

	if err := store.PruneRequestMetricsBefore(ctx, at.Add(-7*24*time.Hour)); err != nil {
		t.Fatalf("PruneRequestMetricsBefore: %v", err)
	}
	old, err := store.RequestMetricsSince(ctx, "p1", at.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("RequestMetricsSince: %v", err)
	}
	if len(old) != 2 {
		t.Errorf("the 8-day-old row is pruned: %+v", old)
	}
}

func TestRequestMetrics_AppendingNothingIsANoop(t *testing.T) {
	if err := testStore(t).AppendRequestMetrics(context.Background(), nil); err != nil {
		t.Errorf("empty append: %v", err)
	}
}
