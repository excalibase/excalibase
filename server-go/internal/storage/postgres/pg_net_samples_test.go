//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

func netSample(at time.Time, app string, rx, tx, busiest float64) netmeter.Sample {
	return netmeter.Sample{At: at, ProjectID: "proj-1", AppID: app, Instances: 2, RxBps: rx, TxBps: tx, MaxInstanceTxBps: busiest}
}

func TestNetSamples_AppendAndListRoundTripOldestFirst(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	batch := []netmeter.Sample{netSample(now, "a", 1, 2, 2), netSample(now.Add(-time.Minute), "a", 3, 4, 3), netSample(now, "b", 9, 9, 9)}
	if err := store.AppendNetSamples(ctx, batch); err != nil {
		t.Fatalf("AppendNetSamples: %v", err)
	}
	got, err := store.ListNetSamples(ctx, "proj-1", "a", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].At.Equal(now.Add(-time.Minute)) || got[0].TxBps != 4 || got[1].RxBps != 1 || got[1].Instances != 2 || got[1].MaxInstanceTxBps != 2 {
		t.Fatalf("samples = %+v", got)
	}
}

func TestNetSamples_AppendOfNothingIsFine(t *testing.T) {
	if err := testStore(t).AppendNetSamples(context.Background(), nil); err != nil {
		t.Fatalf("an empty round: %v", err)
	}
}

func TestNetSamples_PruneRollsFinishedHoursUpAndLeavesTheCurrentHour(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 10, 30, 0, 0, time.UTC)
	var batch []netmeter.Sample
	for minute, tx := range []float64{100, 300} {
		batch = append(batch, netSample(time.Date(2026, 11, 26, 8, minute, 0, 0, time.UTC), "a", 10, tx, tx))
	}
	batch = append(batch, netSample(time.Date(2026, 11, 26, 10, 5, 0, 0, time.UTC), "a", 1, 1, 1))
	if err := store.AppendNetSamples(ctx, batch); err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 2; run++ { // a second run changes nothing
		if err := store.PruneNetSamples(ctx, now); err != nil {
			t.Fatalf("PruneNetSamples: %v", err)
		}
	}
	hours, err := store.ListNetHourly(ctx, "proj-1", "a", now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 1 {
		t.Fatalf("only the finished hour is rolled up: %+v", hours)
	}
	h := hours[0]
	if !h.Hour.Equal(time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)) || h.Samples != 2 || h.AvgTxBps != 200 || h.MaxTxBps != 300 || h.AvgRxBps != 10 || h.MaxInstanceTxBps != 300 {
		t.Errorf("hour = %+v", h)
	}
}

func TestNetSamples_PruneDropsMinutesAfter48HoursAndHoursAfter30Days(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 10, 30, 0, 0, time.UTC)
	old, recent := now.Add(-49*time.Hour), now.Add(-47*time.Hour)
	if err := store.AppendNetSamples(ctx, []netmeter.Sample{netSample(old, "a", 1, 1, 1), netSample(recent, "a", 1, 1, 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO app_net_hourly (hour, project_id, app_id, samples, avg_rx_bps, avg_tx_bps, max_tx_bps, max_instance_tx_bps)
		VALUES ($1,'proj-1','a',60,1,1,1,1), ($2,'proj-1','a',60,1,1,1,1)`, now.Add(-31*24*time.Hour).Truncate(time.Hour), now.Add(-29*24*time.Hour).Truncate(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneNetSamples(ctx, now); err != nil {
		t.Fatal(err)
	}
	minutes, _ := store.ListNetSamples(ctx, "proj-1", "a", now.Add(-72*time.Hour))
	if len(minutes) != 1 || !minutes[0].At.Equal(recent) {
		t.Errorf("minute rows kept: %+v", minutes)
	}
	hours, _ := store.ListNetHourly(ctx, "proj-1", "a", now.Add(-40*24*time.Hour))
	for _, h := range hours {
		if h.Hour.Before(now.Add(-30 * 24 * time.Hour)) {
			t.Errorf("an hourly row past 30 days survived: %+v", h)
		}
	}
	rolled := false
	for _, h := range hours {
		rolled = rolled || h.Hour.Equal(now.Add(-29*24*time.Hour).Truncate(time.Hour))
	}
	if !rolled {
		t.Errorf("the 29-day-old hourly row was dropped: %+v", hours)
	}
}

func TestNetSamples_PruneDeletesInBatches(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	previous := netPruneBatch
	netPruneBatch = 7
	defer func() { netPruneBatch = previous }()
	now := time.Date(2026, 11, 26, 10, 30, 0, 0, time.UTC)
	var batch []netmeter.Sample
	for i := 0; i < 30; i++ {
		batch = append(batch, netSample(now.Add(-72*time.Hour+time.Duration(i)*time.Minute), "a", 1, 1, 1))
	}
	if err := store.AppendNetSamples(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneNetSamples(ctx, now); err != nil {
		t.Fatal(err)
	}
	left, _ := store.ListNetSamples(ctx, "proj-1", "a", now.Add(-100*time.Hour))
	if len(left) != 0 {
		t.Fatalf("%d rows survived a batched delete of 7 at a time", len(left))
	}
}

func TestNetSamples_TablesHaveNoForeignKeysSoEvidenceSurvivesDeletion(t *testing.T) {
	store := testStore(t)
	var count int
	err := store.DB().QueryRow(`SELECT COUNT(*) FROM information_schema.table_constraints
		WHERE constraint_type = 'FOREIGN KEY' AND table_name IN ('app_net_samples', 'app_net_hourly')`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatalf("foreign keys on the metering tables: %d %v", count, err)
	}
}

// A finished hour is rolled up once and then left alone. Minute rows go only by
// whole hours, so the hour at the 48 hour edge is never half there when a later
// prune looks at it (the rollup used to rebuild it from the surviving half).
func TestNetSamples_PruneNeverRebuildsAnHourFromItsSurvivingHalf(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	hour := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	var batch []netmeter.Sample
	for minute := 0; minute < 60; minute++ {
		tx := 100.0
		if minute == 10 {
			tx = 9000 // the burst, in the half a partial delete would remove
		}
		batch = append(batch, netSample(hour.Add(time.Duration(minute)*time.Minute), "a", 10, tx, tx))
	}
	if err := store.AppendNetSamples(ctx, batch); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{48*time.Hour + 30*time.Minute, 48*time.Hour + 45*time.Minute, 49*time.Hour + 30*time.Minute} {
		if err := store.PruneNetSamples(ctx, hour.Add(offset)); err != nil {
			t.Fatal(err)
		}
		hours, err := store.ListNetHourly(ctx, "proj-1", "a", hour.Add(-time.Hour))
		if err != nil || len(hours) != 1 {
			t.Fatalf("at +%v: %+v %v", offset, hours, err)
		}
		if hours[0].Samples != 60 || hours[0].MaxTxBps != 9000 {
			t.Fatalf("at +%v the hour lost its burst: %+v", offset, hours[0])
		}
	}
	minutes, _ := store.ListNetSamples(ctx, "proj-1", "a", hour.Add(-time.Hour))
	if len(minutes) != 0 {
		t.Fatalf("the whole hour goes together once it is 48 hours old: %d minutes left", len(minutes))
	}
}

func TestNetSamples_PruneKeepsAWholeHourWhileAnyOfItIsYoungerThan48Hours(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	hour := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	if err := store.AppendNetSamples(ctx, []netmeter.Sample{netSample(hour.Add(5*time.Minute), "a", 1, 1, 1), netSample(hour.Add(50*time.Minute), "a", 1, 1, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneNetSamples(ctx, hour.Add(48*time.Hour+20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	minutes, _ := store.ListNetSamples(ctx, "proj-1", "a", hour.Add(-time.Hour))
	if len(minutes) != 2 {
		t.Fatalf("minute rows are cut at an hour boundary, not at now minus 48 h: %d left", len(minutes))
	}
}

func TestNetSamples_PruneRollsOnlyHoursAfterTheLastRolledOne(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 10, 30, 0, 0, time.UTC)
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO app_net_hourly (hour, project_id, app_id, samples, avg_rx_bps, avg_tx_bps, max_tx_bps, max_instance_tx_bps)
		VALUES ($1,'proj-1','a',60,1,1,1,1)`, time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	// An hour already rolled is not touched even if stray minutes of it are still there.
	stray := netSample(time.Date(2026, 11, 26, 8, 15, 0, 0, time.UTC), "a", 5, 5000, 5000)
	next := netSample(time.Date(2026, 11, 26, 9, 15, 0, 0, time.UTC), "a", 5, 400, 400)
	if err := store.AppendNetSamples(ctx, []netmeter.Sample{stray, next}); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneNetSamples(ctx, now); err != nil {
		t.Fatal(err)
	}
	hours, _ := store.ListNetHourly(ctx, "proj-1", "a", now.Add(-24*time.Hour))
	if len(hours) != 2 || hours[0].Samples != 60 || hours[0].MaxTxBps != 1 || hours[1].Samples != 1 || hours[1].MaxTxBps != 400 {
		t.Fatalf("hours = %+v: 8:00 stays as it was, 9:00 is rolled", hours)
	}
}
