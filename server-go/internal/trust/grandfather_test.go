package trust

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeGrandfather struct {
	granted   int
	err       error
	cutoffs   []time.Time
	recorded  []BackfillRun
	recordErr error
	callOrder []string
}

func (f *fakeGrandfather) GrandfatherUsersBefore(_ context.Context, cutoff time.Time) (int, error) {
	f.callOrder = append(f.callOrder, "grandfather")
	f.cutoffs = append(f.cutoffs, cutoff)
	return f.granted, f.err
}

func (f *fakeGrandfather) RecordBackfill(_ context.Context, run BackfillRun) error {
	f.callOrder = append(f.callOrder, "record")
	f.recorded = append(f.recorded, run)
	return f.recordErr
}

func TestRunBackfillGrandfathersThenRecordsTheMarkerWithTheCutoff(t *testing.T) {
	store := &fakeGrandfather{granted: 278}
	now := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	granted, err := RunBackfill(context.Background(), store, testCutoff, now)
	if err != nil || granted != 278 {
		t.Fatalf("got %d %v", granted, err)
	}
	if len(store.cutoffs) != 1 || !store.cutoffs[0].Equal(testCutoff) {
		t.Errorf("cutoffs %v, want the configured one", store.cutoffs)
	}
	if len(store.recorded) != 1 || !store.recorded[0].Cutoff.Equal(testCutoff) || !store.recorded[0].FinishedAt.Equal(now) {
		t.Errorf("marker %+v", store.recorded)
	}
	if store.callOrder[0] != "grandfather" || store.callOrder[1] != "record" {
		t.Errorf("order %v: the marker must follow the work", store.callOrder)
	}
}

// A failed backfill leaves no marker, so the boot guard keeps the flag off.
func TestRunBackfillRecordsNoMarkerWhenTheWorkFails(t *testing.T) {
	store := &fakeGrandfather{err: errors.New("db down")}
	if _, err := RunBackfill(context.Background(), store, testCutoff, time.Now()); err == nil {
		t.Fatal("a failed backfill reported success")
	}
	if len(store.recorded) != 0 {
		t.Errorf("recorded a marker after a failure: %+v", store.recorded)
	}
}

func TestRunBackfillRefusesAnEmptyOrFutureCutoff(t *testing.T) {
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	for name, cutoff := range map[string]time.Time{"empty": {}, "future": now.Add(time.Hour)} {
		store := &fakeGrandfather{}
		if _, err := RunBackfill(context.Background(), store, cutoff, now); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("%s: got %v", name, err)
		}
		if len(store.callOrder) != 0 {
			t.Errorf("%s: touched the store: %v", name, store.callOrder)
		}
	}
}
