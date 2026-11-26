package trust

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var testCutoff = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

type fakeMarkers struct {
	run   *BackfillRun
	err   error
	reads int
}

func (f *fakeMarkers) LatestBackfill(context.Context) (*BackfillRun, error) {
	f.reads++
	return f.run, f.err
}

func TestFlagOnWithoutABackfillRefusesToStart(t *testing.T) {
	err := RequireBackfill(context.Background(), on(), &fakeMarkers{}, testCutoff)
	if !errors.Is(err, ErrBackfillMissing) {
		t.Fatalf("got %v, want ErrBackfillMissing", err)
	}
	for _, word := range []string{"FEATURE_LIMITEDTRIAL", "backfill"} {
		if !strings.Contains(err.Error(), word) {
			t.Errorf("the error does not explain itself (missing %q): %v", word, err)
		}
	}
}

func TestFlagOnWithABackfillStarts(t *testing.T) {
	run := &BackfillRun{FinishedAt: time.Now(), Cutoff: testCutoff}
	if err := RequireBackfill(context.Background(), on(), &fakeMarkers{run: run}, testCutoff); err != nil {
		t.Errorf("refused although the backfill ran: %v", err)
	}
}

func TestFlagOffNeverLooksForABackfill(t *testing.T) {
	markers := &fakeMarkers{err: errors.New("must not be read")}
	if err := RequireBackfill(context.Background(), off(), markers, testCutoff); err != nil || markers.reads != 0 {
		t.Errorf("flag off: %v, reads=%d", err, markers.reads)
	}
}

func TestFlagOnFailsClosedWhenTheMarkerCannotBeRead(t *testing.T) {
	cause := errors.New("db down")
	if err := RequireBackfill(context.Background(), on(), &fakeMarkers{err: cause}, testCutoff); !errors.Is(err, cause) {
		t.Errorf("read error: %v", err)
	}
	if err := RequireBackfill(context.Background(), on(), nil, testCutoff); !errors.Is(err, ErrBackfillMissing) {
		t.Errorf("no marker source: %v", err)
	}
}

// A backfill that used another cutoff granted full to a different set of accounts than the one configured now.
func TestAMarkerWithAnotherCutoffRefusesToStart(t *testing.T) {
	run := &BackfillRun{FinishedAt: time.Now(), Cutoff: testCutoff.AddDate(0, 0, -7).In(time.FixedZone("pst", -8*3600))}
	err := RequireBackfill(context.Background(), on(), &fakeMarkers{run: run}, testCutoff)
	if !errors.Is(err, ErrBackfillCutoffMismatch) {
		t.Fatalf("got %v, want ErrBackfillCutoffMismatch", err)
	}
	for _, word := range []string{"TRUST_GRANDFATHER_BEFORE", "2026-10-25", "2026-11-01"} {
		if !strings.Contains(err.Error(), word) {
			t.Errorf("the error does not name %q: %v", word, err)
		}
	}
}

func TestACutoffInAnotherZoneButTheSameInstantIsTheSame(t *testing.T) {
	zone := time.FixedZone("ict", 7*3600)
	run := &BackfillRun{FinishedAt: time.Now(), Cutoff: testCutoff.In(zone)}
	if err := RequireBackfill(context.Background(), on(), &fakeMarkers{run: run}, testCutoff); err != nil {
		t.Errorf("same instant refused: %v", err)
	}
}
