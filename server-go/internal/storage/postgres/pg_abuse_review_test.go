//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/behaviour"
	"github.com/excalibase/provisioning-poc/internal/netmeter"
)

var reviewRules = behaviour.Rules{MuteDays: 14, GrowthFactor: 2}

func reviewFinding(app string, magnitude float64) behaviour.Finding {
	return behaviour.Finding{
		Subject: behaviour.Subject{OwnerID: "u-cust", OrgID: "o-cust", ProjectID: "p-cust", ProjectName: "Shop", AppID: app, AppName: "web", Tier: "free"},
		Signal:  behaviour.SignalRelaySymmetry, Magnitude: magnitude,
		Evidence: map[string]float64{"ratio": 1, "avgTxBps": magnitude * 1000, "percentOfReference": magnitude},
	}
}

func record(t *testing.T, store *Store, f behaviour.Finding, at time.Time) behaviour.Outcome {
	t.Helper()
	outcome, err := store.RecordFinding(context.Background(), f, at, reviewRules)
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}
	return outcome
}

func listAll(t *testing.T, store *Store) []behaviour.Review {
	t.Helper()
	reviews, err := store.ListReviews(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return reviews
}

func TestReviews_OneOpenRowPerOwnerAppAndSignalRepeatsOnlyUpdateIt(t *testing.T) {
	store := testStore(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	if got := record(t, store, reviewFinding("a1", 60), t0); got != behaviour.OutcomeOpened {
		t.Fatalf("first finding: %v", got)
	}
	if got := record(t, store, reviewFinding("a1", 75), t0.Add(5*time.Minute)); got != behaviour.OutcomeUpdated {
		t.Fatalf("a repeat: %v", got)
	}
	record(t, store, reviewFinding("a1", 65), t0.Add(10*time.Minute))
	reviews := listAll(t, store)
	if len(reviews) != 1 {
		t.Fatalf("one open row, got %d", len(reviews))
	}
	r := reviews[0]
	if !r.FirstSeen.Equal(t0) || !r.LastSeen.Equal(t0.Add(10*time.Minute)) || r.Peak != 75 || r.Evidence["percentOfReference"] != 65 {
		t.Fatalf("first seen stays, last seen and evidence follow, the peak is the highest: %+v", r)
	}
	record(t, store, reviewFinding("a2", 60), t0)
	other := reviewFinding("a1", 60)
	other.Signal = behaviour.SignalSustainedEgress
	record(t, store, other, t0)
	if got := len(listAll(t, store)); got != 3 {
		t.Fatalf("another app and another signal are their own rows: %d", got)
	}
}

func TestReviews_ARepeatRefreshesTheSnapshotOfNamesAndTier(t *testing.T) {
	store := testStore(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	record(t, store, reviewFinding("a1", 60), t0)
	renamed := reviewFinding("a1", 60)
	renamed.AppName, renamed.ProjectName, renamed.Tier = "web-v2", "Shop 2", "standard"
	record(t, store, renamed, t0.Add(time.Minute))
	r := listAll(t, store)[0]
	if r.AppName != "web-v2" || r.ProjectName != "Shop 2" || r.Tier != "standard" || r.OwnerID != "u-cust" {
		t.Fatalf("the names and tier follow the latest finding, the owner stays: %+v", r)
	}
}

func TestReviews_AnOwnerLevelFindingIsItsOwnRow(t *testing.T) {
	store := testStore(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	record(t, store, reviewFinding("a1", 60), t0)
	ownerLevel := reviewFinding("", 90)
	ownerLevel.ProjectID, ownerLevel.ProjectName = "", ""
	record(t, store, ownerLevel, t0)
	record(t, store, ownerLevel, t0.Add(time.Minute))
	if got := len(listAll(t, store)); got != 2 {
		t.Fatalf("rows = %d", got)
	}
}

func TestReviews_DismissMutesForDaysUnlessTheFindingGrows(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	record(t, store, reviewFinding("a1", 60), t0)
	id := listAll(t, store)[0].ID
	if err := store.DismissReview(ctx, id, "admin-1", t0.Add(time.Hour), 14); err != nil {
		t.Fatal(err)
	}
	if got := record(t, store, reviewFinding("a1", 70), t0.Add(2*time.Hour)); got != behaviour.OutcomeSuppressed {
		t.Fatalf("a smaller repeat inside the mute: %v", got)
	}
	if got := record(t, store, reviewFinding("a1", 119), t0.Add(24*time.Hour)); got != behaviour.OutcomeSuppressed {
		t.Fatalf("just under double the dismissed peak, still muted: %v", got)
	}
	if got := record(t, store, reviewFinding("a1", 130), t0.Add(25*time.Hour)); got != behaviour.OutcomeOpened {
		t.Fatalf("past double the dismissed peak opens a new review: %v", got)
	}
	reviews := listAll(t, store)
	var open, dismissed int
	for _, r := range reviews {
		switch r.Status {
		case behaviour.StatusOpen:
			open++
		case behaviour.StatusDismissed:
			dismissed++
			if r.DismissedBy != "admin-1" || r.DismissedPeak != 60 || r.MuteUntil == nil || !r.MuteUntil.Equal(t0.Add(time.Hour+14*24*time.Hour)) {
				t.Errorf("the dismissed row keeps who, the peak at the time and until when: %+v", r)
			}
		}
	}
	if open != 1 || dismissed != 1 {
		t.Fatalf("open %d dismissed %d", open, dismissed)
	}
}

func TestReviews_AfterTheMuteAnyFindingOpensANewReview(t *testing.T) {
	store := testStore(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	record(t, store, reviewFinding("a1", 60), t0)
	if err := store.DismissReview(context.Background(), listAll(t, store)[0].ID, "admin-1", t0, 14); err != nil {
		t.Fatal(err)
	}
	if got := record(t, store, reviewFinding("a1", 50), t0.Add(15*24*time.Hour)); got != behaviour.OutcomeOpened {
		t.Fatalf("the mute ran out: %v", got)
	}
}

func TestReviews_DismissOfAnUnknownOrClosedReviewIsAnError(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	if err := store.DismissReview(ctx, 999, "admin-1", t0, 14); err == nil {
		t.Fatal("an unknown review cannot be dismissed")
	}
	record(t, store, reviewFinding("a1", 60), t0)
	id := listAll(t, store)[0].ID
	if err := store.DismissReview(ctx, id, "admin-1", t0, 14); err != nil {
		t.Fatal(err)
	}
	if err := store.DismissReview(ctx, id, "admin-2", t0, 14); err == nil {
		t.Fatal("a review already dismissed is not dismissed twice")
	}
}

func TestReviews_RefuseEvidenceOutsideTheFixedNumericKeys(t *testing.T) {
	store := testStore(t)
	f := reviewFinding("a1", 60)
	f.Evidence["destination"] = 1
	if _, err := store.RecordFinding(context.Background(), f, time.Now(), reviewRules); err == nil {
		t.Fatal("evidence may carry only the fixed numeric keys")
	}
}

func TestReviews_SurviveTheAppProjectAndAccountBeingDeleted(t *testing.T) {
	store, _ := consoleFixture(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	record(t, store, reviewFinding("a-run", 60), t0)
	if _, err := store.db.ExecContext(ctx, `DELETE FROM apps WHERE project_id = 'p-cust'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM database_instances WHERE project_id = 'p-cust'`); err != nil {
		t.Fatal(err)
	}
	reviews := listAll(t, store)
	if len(reviews) != 1 || reviews[0].OwnerID != "u-cust" || reviews[0].ProjectName != "Shop" || reviews[0].AppName != "web" {
		t.Fatalf("the snapshot stays: %+v", reviews)
	}
	var foreignKeys int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_type = 'FOREIGN KEY' AND table_name = 'abuse_review'`).Scan(&foreignKeys); err != nil || foreignKeys != 0 {
		t.Fatalf("foreign keys on abuse_review: %d %v", foreignKeys, err)
	}
}

func TestReviews_PruneDropsOldDismissedOnlyKeepsOpenAndActioned(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	record(t, store, reviewFinding("old-dismissed", 60), t0)
	record(t, store, reviewFinding("new-dismissed", 60), t0)
	record(t, store, reviewFinding("open", 60), t0)
	record(t, store, reviewFinding("actioned", 60), t0)
	ids := map[string]int64{}
	for _, r := range listAll(t, store) {
		ids[r.AppID] = r.ID
	}
	if err := store.DismissReview(ctx, ids["old-dismissed"], "a", t0.Add(-200*24*time.Hour), 14); err != nil {
		t.Fatal(err)
	}
	if err := store.DismissReview(ctx, ids["new-dismissed"], "a", t0.Add(-10*24*time.Hour), 14); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE abuse_review SET status = 'actioned', dismissed_at = $2 WHERE id = $1`, ids["actioned"], t0.Add(-400*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneReviews(ctx, t0, 180); err != nil {
		t.Fatal(err)
	}
	left := map[string]string{}
	for _, r := range listAll(t, store) {
		left[r.AppID] = r.Status
	}
	if len(left) != 3 || left["old-dismissed"] != "" || left["new-dismissed"] != "dismissed" || left["open"] != "open" || left["actioned"] != "actioned" {
		t.Fatalf("left = %v", left)
	}
}

func TestReviews_ListFiltersByStatus(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	record(t, store, reviewFinding("a1", 60), t0)
	record(t, store, reviewFinding("a2", 60), t0)
	if err := store.DismissReview(ctx, listAll(t, store)[0].ID, "x", t0, 14); err != nil {
		t.Fatal(err)
	}
	open, _ := store.ListReviews(ctx, behaviour.StatusOpen)
	dismissed, _ := store.ListReviews(ctx, behaviour.StatusDismissed)
	if len(open) != 1 || len(dismissed) != 1 {
		t.Fatalf("open %d dismissed %d", len(open), len(dismissed))
	}
}

func TestNetHistory_RecentMinutesAndVolumes(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 12, 30, 0, 0, time.UTC)
	hour := now.Add(-5 * time.Hour).Truncate(time.Hour) // a finished, rolled-up hour
	if _, err := store.db.ExecContext(ctx, `INSERT INTO app_net_hourly (hour, project_id, app_id, samples, avg_rx_bps, avg_tx_bps, max_tx_bps, max_instance_tx_bps)
		VALUES ($1, 'p-cust', 'a-run', 30, 100, 200, 300, 300), ($2, 'p-cust', 'a-run', 30, 100, 200, 300, 300)`,
		hour, now.Add(-20*24*time.Hour).Truncate(time.Hour)); err != nil {
		t.Fatal(err)
	}
	samples := []netmeter.Sample{
		netSample(now.Add(-10*time.Minute), "a-run", 10, 20, 20), // in the window, after the last rolled hour
		netSample(now.Add(-2*time.Minute), "a-run", 10, 20, 20),
		netSample(now.Add(-3*time.Hour), "a-run", 10, 20, 20),  // in the day, not the window
		netSample(now.Add(-30*time.Hour), "a-run", 10, 20, 20), // not in the day, and inside an hour already rolled
	}
	for i := range samples {
		samples[i].ProjectID = "p-cust"
	}
	if err := store.AppendNetSamples(ctx, samples); err != nil {
		t.Fatal(err)
	}
	recent, err := store.RecentNetMinutes(ctx, now.Add(-60*time.Minute))
	if err != nil || len(recent) != 2 || !recent[0].At.Before(recent[1].At) {
		t.Fatalf("recent = %+v %v: the window only, oldest first", recent, err)
	}
	volumes, err := store.NetVolumes(ctx, now)
	if err != nil || len(volumes) != 1 {
		t.Fatalf("volumes = %+v %v", volumes, err)
	}
	v := volumes[0]
	// day: three minute rows within 24 h at 20 B/s tx for 60 s; month: two hourly rows (200 B/s x 30 sampled minutes x 60 s, not a full hour) plus the minutes after the last rolled hour.
	if v.ProjectID != "p-cust" || v.AppID != "a-run" || v.TxDay != 3*20*60 || v.RxDay != 3*10*60 {
		t.Fatalf("day = %+v", v)
	}
	wantMonth := 2*200.0*30*60 + 3*20*60
	if v.Tx30d != wantMonth {
		t.Fatalf("30 day tx = %v, want %v", v.Tx30d, wantMonth)
	}
}

func TestReviewDirectory_NamesEachAppAndProjectWithItsOwnerAndTier(t *testing.T) {
	store, _ := consoleFixture(t)
	apps, projects, err := store.ReviewDirectory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byApp := map[string]behaviour.Subject{}
	for _, a := range apps {
		byApp[a.ProjectID+"/"+a.AppID] = a
	}
	run := byApp["p-cust/a-run"]
	if len(apps) != 2 || run.OwnerID != "u-cust" || run.OrgID != "o-cust" || run.ProjectName != "p-cust" || run.AppName != "a-run" || run.Tier != "free" {
		t.Fatalf("apps = %+v", apps)
	}
	if projects["p-other"].OwnerID != "u-other" || projects["p-cust"].AppID != "" {
		t.Fatalf("projects = %+v", projects)
	}
}

func stampedSample(at time.Time, app string, tx float64) netmeter.Sample {
	sample := netSample(at, app, tx, tx, tx)
	sample.ProjectID, sample.OwnerID, sample.Tier = "p-cust", "u-cust", "free"
	return sample
}

func TestProjectOwners_NamesEachProjectsOwnerAndTier(t *testing.T) {
	store, _ := consoleFixture(t)
	owners, err := store.ProjectOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if owners["p-cust"] != (netmeter.Owner{ID: "u-cust", Tier: "free"}) || owners["p-other"].ID != "u-other" {
		t.Fatalf("owners = %+v", owners)
	}
}

// HIGH: deleting a project must not reset its owner's evidence. The owner and
// tier live on the minute rows, the hourly rollups and the volumes read from them.
func TestNetHistory_AnOwnersTrafficSurvivesTheProjectBeingDeleted(t *testing.T) {
	store, _ := consoleFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 12, 30, 0, 0, time.UTC)
	rolledHour := now.Add(-5 * time.Hour).Truncate(time.Hour)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO app_net_hourly (hour, project_id, app_id, samples, avg_rx_bps, avg_tx_bps, max_tx_bps, max_instance_tx_bps, owner_id, tier)
		VALUES ($1, 'p-cust', 'a-run', 60, 100, 200, 300, 300, 'u-cust', 'free')`, rolledHour); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendNetSamples(ctx, []netmeter.Sample{stampedSample(now.Add(-10*time.Minute), "a-run", 20), stampedSample(now.Add(-2*time.Minute), "a-run", 20)}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DELETE FROM apps WHERE project_id = 'p-cust'`, `DELETE FROM database_instances WHERE project_id = 'p-cust'`} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	minutes, err := store.RecentNetMinutes(ctx, now.Add(-time.Hour))
	if err != nil || len(minutes) != 2 || minutes[0].OwnerID != "u-cust" || minutes[0].Tier != "free" {
		t.Fatalf("minutes = %+v %v", minutes, err)
	}
	hours, err := store.RecentNetHours(ctx, now.Add(-24*time.Hour))
	if err != nil || len(hours) != 1 || hours[0].OwnerID != "u-cust" || hours[0].Tier != "free" || hours[0].AvgTxBps != 20 || hours[0].Samples != 2 {
		t.Fatalf("hours = %+v %v", hours, err)
	}
	volumes, err := store.NetVolumes(ctx, now)
	if err != nil || len(volumes) != 1 || volumes[0].OwnerID != "u-cust" || volumes[0].Tier != "free" {
		t.Fatalf("volumes = %+v %v", volumes, err)
	}
	if owners, _ := store.ProjectOwners(ctx); len(owners) != 1 || owners["p-cust"].ID != "" {
		t.Fatalf("the directory forgot the project, as it does: %+v", owners)
	}
}

func TestNetSamples_RollUpKeepsTheOwnerAndTier(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 26, 12, 30, 0, 0, time.UTC)
	first := now.Add(-3 * time.Hour).Truncate(time.Hour)
	var batch []netmeter.Sample
	for i := 0; i < 3; i++ {
		sample := stampedSample(first.Add(time.Duration(i)*time.Minute), "a-run", 10)
		sample.ProjectID = "proj-1"
		batch = append(batch, sample)
	}
	if err := store.AppendNetSamples(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneNetSamples(ctx, now); err != nil {
		t.Fatal(err)
	}
	hourly, err := store.ListNetHourly(ctx, "proj-1", "a-run", first.Add(-time.Hour))
	if err != nil || len(hourly) != 1 || hourly[0].OwnerID != "u-cust" || hourly[0].Tier != "free" {
		t.Fatalf("hourly = %+v %v", hourly, err)
	}
}

func TestReviews_AnOwnerLevelFindingIsOneRowPerTierBucket(t *testing.T) {
	store := testStore(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	for _, tier := range []string{"free", "standard"} {
		f := reviewFinding("", 90)
		f.Tier = tier
		record(t, store, f, t0)
		record(t, store, f, t0.Add(time.Minute))
	}
	reviews := listAll(t, store)
	if len(reviews) != 2 {
		t.Fatalf("the Free bucket and the Standard bucket of one owner are two rows: %+v", reviews)
	}
	for _, r := range reviews {
		if r.Tier == "free" {
			if err := store.DismissReview(context.Background(), r.ID, "a", t0, 14); err != nil {
				t.Fatal(err)
			}
		}
	}
	free, standard := reviewFinding("", 90), reviewFinding("", 90)
	free.Tier, standard.Tier = "free", "standard"
	if got := record(t, store, free, t0.Add(time.Hour)); got != behaviour.OutcomeSuppressed {
		t.Fatalf("the Free bucket's dismissal mutes the Free bucket: %v", got)
	}
	if got := record(t, store, standard, t0.Add(time.Hour)); got != behaviour.OutcomeUpdated {
		t.Fatalf("and leaves the Standard bucket's open row alone: %v", got)
	}
}

func TestReviews_ADismissedBucketDoesNotMuteTheOwnersOtherBucket(t *testing.T) {
	store := testStore(t)
	t0 := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	free, standard := reviewFinding("", 90), reviewFinding("", 90)
	free.Tier, standard.Tier = "free", "standard"
	record(t, store, free, t0)
	if err := store.DismissReview(context.Background(), listAll(t, store)[0].ID, "a", t0, 14); err != nil {
		t.Fatal(err)
	}
	if got := record(t, store, standard, t0.Add(time.Hour)); got != behaviour.OutcomeOpened {
		t.Fatalf("the same signal in the Standard bucket is its own finding: %v", got)
	}
}
