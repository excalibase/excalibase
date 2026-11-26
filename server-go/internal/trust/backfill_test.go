package trust

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

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
	err := RequireBackfill(context.Background(), on(), &fakeMarkers{})
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
	run := &BackfillRun{FinishedAt: time.Now(), Cutoff: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}
	if err := RequireBackfill(context.Background(), on(), &fakeMarkers{run: run}); err != nil {
		t.Errorf("refused although the backfill ran: %v", err)
	}
}

func TestFlagOffNeverLooksForABackfill(t *testing.T) {
	markers := &fakeMarkers{err: errors.New("must not be read")}
	if err := RequireBackfill(context.Background(), off(), markers); err != nil || markers.reads != 0 {
		t.Errorf("flag off: %v, reads=%d", err, markers.reads)
	}
}

func TestFlagOnFailsClosedWhenTheMarkerCannotBeRead(t *testing.T) {
	cause := errors.New("db down")
	if err := RequireBackfill(context.Background(), on(), &fakeMarkers{err: cause}); !errors.Is(err, cause) {
		t.Errorf("read error: %v", err)
	}
	if err := RequireBackfill(context.Background(), on(), nil); !errors.Is(err, ErrBackfillMissing) {
		t.Errorf("no marker source: %v", err)
	}
}
