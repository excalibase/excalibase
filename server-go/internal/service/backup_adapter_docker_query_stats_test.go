package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-673: a single-host copy restored while slowqueries is on carries
// statistics, as a restored Kubernetes cluster does (EXC-654).

func createdStatsExtension(stdins []string) bool {
	for _, stdin := range stdins {
		if strings.Contains(stdin, "CREATE EXTENSION IF NOT EXISTS pg_stat_statements") {
			return true
		}
	}
	return false
}

func TestDockerRestoreWithQueryStatsTurnsThemOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		adapter, store, _, uploader, records := setupDockerAdapter(t)
		adapter.SetInstanceStore(store)
		dc := &fakeDockerClientForAdapter{}
		adapter.SetDockerClient(dc)
		adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
		adapter.SetQueryStats(on)
		seedDockerBackups(t, uploader, records, map[string]time.Time{"backup-2026": time.Now().Add(-time.Hour)})
		src, _ := store.FindByProjectID("dk-1")
		if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dk-copy", TargetProjectID: "dk-copy"}); err != nil {
			t.Fatalf("feature %v: Restore: %v", on, err)
		}
		if got := createdStatsExtension(dc.stdins); got != on {
			t.Errorf("feature %v: restored copy statistics = %v", on, got)
		}
	}
}

func TestDockerInPlaceRestoreWithQueryStatsKeepsThemOn(t *testing.T) {
	f := newInPlaceFixture(t)
	f.adapter.SetQueryStats(true)
	ctx := context.Background()
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{TargetTime: &domain.ZonedTime{Time: time.Now().Add(-time.Hour)}})
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if !createdStatsExtension(f.dc.stdins) {
		t.Error("the rebuilt database lost its statistics")
	}
}
