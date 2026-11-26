package netmeter

import (
	"context"
	"errors"
	"testing"
	"time"
)

type scriptedSource struct {
	rounds [][]Reading
	err    error
	calls  int
}

func (s *scriptedSource) Read(context.Context) ([]Reading, error) {
	i := s.calls
	s.calls++
	if i >= len(s.rounds) {
		return nil, s.err
	}
	return s.rounds[i], s.err
}

type memoryStore struct {
	appended  [][]Sample
	pruned    []time.Time
	appendErr error
	pruneErr  error
}

func (m *memoryStore) AppendNetSamples(_ context.Context, samples []Sample) error {
	m.appended = append(m.appended, samples)
	return m.appendErr
}
func (m *memoryStore) PruneNetSamples(_ context.Context, now time.Time) error {
	m.pruned = append(m.pruned, now)
	return m.pruneErr
}

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func newTestMeter(source Source, store Store, c *clock) *Meter {
	meter := NewMeter(source, store, time.Minute)
	meter.now = c.Now
	return meter
}

func round(at time.Time, rx, tx uint64) []Reading {
	return []Reading{{Key: "a", ProjectID: "p", AppID: "x", At: at, Rx: rx, Tx: tx}}
}

func TestMeter_FirstSampleStoresNothingSecondStoresTheRate(t *testing.T) {
	source := &scriptedSource{rounds: [][]Reading{round(t0, 0, 0), round(t0.Add(time.Minute), 6000, 12000)}}
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(source, store, c)
	meter.Sample(context.Background())
	if len(store.appended) != 0 {
		t.Fatalf("the first sample is a baseline: %+v", store.appended)
	}
	c.now = t0.Add(time.Minute)
	meter.Sample(context.Background())
	if len(store.appended) != 1 || store.appended[0][0].RxBps != 100 || store.appended[0][0].TxBps != 200 {
		t.Fatalf("stored = %+v", store.appended)
	}
	if !store.appended[0][0].At.Equal(t0.Add(time.Minute)) {
		t.Errorf("a sample is stamped with the clock: %+v", store.appended[0][0])
	}
}

func TestMeter_ALongGapIsALeaderChangeAndSkipsOneSample(t *testing.T) {
	source := &scriptedSource{rounds: [][]Reading{
		round(t0, 0, 0), round(t0.Add(time.Minute), 60, 60),
		round(t0.Add(time.Hour), 999999, 999999), round(t0.Add(time.Hour+time.Minute), 1000059, 1000059),
	}}
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(source, store, c)
	for _, step := range []time.Duration{0, time.Minute, time.Hour, time.Hour + time.Minute} {
		c.now = t0.Add(step)
		meter.Sample(context.Background())
	}
	if len(store.appended) != 2 {
		t.Fatalf("minute 1 and the sample after the baseline: %d batches", len(store.appended))
	}
	if got := store.appended[1][0].TxBps; got != 1 {
		t.Errorf("the hour-long gap was not averaged into a rate: tx %v", got)
	}
}

func TestMeter_SourceErrorStoresNothingAndIsNotFatal(t *testing.T) {
	store := &memoryStore{}
	meter := newTestMeter(&scriptedSource{err: errors.New("kubelet down")}, store, &clock{now: t0})
	meter.Sample(context.Background())
	if len(store.appended) != 0 {
		t.Fatalf("stored on error: %+v", store.appended)
	}
}

func TestMeter_PartialReadingsWithErrorStillCount(t *testing.T) {
	source := &scriptedSource{rounds: [][]Reading{round(t0, 0, 0), round(t0.Add(time.Minute), 60, 60)}}
	partial := &partialSource{inner: source, err: errors.New("one node down")}
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(partial, store, c)
	meter.Sample(context.Background())
	c.now = t0.Add(time.Minute)
	meter.Sample(context.Background())
	if len(store.appended) != 1 {
		t.Fatalf("readings from the healthy nodes are used: %d", len(store.appended))
	}
}

type partialSource struct {
	inner *scriptedSource
	err   error
}

func (p *partialSource) Read(ctx context.Context) ([]Reading, error) {
	readings, _ := p.inner.Read(ctx)
	return readings, p.err
}

func TestMeter_StoreErrorsAreSurvived(t *testing.T) {
	source := &scriptedSource{rounds: [][]Reading{round(t0, 0, 0), round(t0.Add(time.Minute), 60, 60)}}
	store := &memoryStore{appendErr: errors.New("db down"), pruneErr: errors.New("db down")}
	c := &clock{now: t0}
	meter := newTestMeter(source, store, c)
	meter.Sample(context.Background())
	c.now = t0.Add(time.Minute)
	meter.Sample(context.Background())
}

func TestMeter_PrunesAtMostOncePerHour(t *testing.T) {
	source := &scriptedSource{}
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(source, store, c)
	for _, step := range []time.Duration{0, time.Minute, 30 * time.Minute, time.Hour + time.Minute} {
		c.now = t0.Add(step)
		meter.Sample(context.Background())
	}
	if len(store.pruned) != 2 {
		t.Fatalf("pruned at start and again after an hour, got %d: %v", len(store.pruned), store.pruned)
	}
}

func TestMeter_ATotalFailureDoesNotPoisonTheNextRound(t *testing.T) {
	failing := &flakySource{rounds: []flakyRound{
		{readings: round(t0, 0, 0)},
		{err: errors.New("kubelet down")},
		{readings: round(t0.Add(2*time.Minute), 12000, 12000)},
	}}
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(failing, store, c)
	for _, step := range []time.Duration{0, time.Minute, 2 * time.Minute} {
		c.now = t0.Add(step)
		meter.Sample(context.Background())
	}
	if len(store.appended) != 1 || store.appended[0][0].RxBps != 100 {
		t.Fatalf("the round after a failed one is rated from the baseline that survived it: %+v", store.appended)
	}
}

func TestMeter_APartialFailureKeepsTheBaselineOfWhatItDidNotSee(t *testing.T) {
	both := []Reading{{Key: "a", ProjectID: "p", AppID: "x", At: t0}, {Key: "b", ProjectID: "p", AppID: "y", At: t0}}
	source := &flakySource{rounds: []flakyRound{
		{readings: both},
		{readings: []Reading{{Key: "b", ProjectID: "p", AppID: "y", At: t0.Add(time.Minute), Rx: 60, Tx: 60}}, err: &PartialError{Unobserved: []string{"a"}, Err: errors.New("node a down")}},
		{readings: []Reading{
			{Key: "a", ProjectID: "p", AppID: "x", At: t0.Add(2 * time.Minute), Rx: 12000, Tx: 12000},
			{Key: "b", ProjectID: "p", AppID: "y", At: t0.Add(2 * time.Minute), Rx: 120, Tx: 120},
		}},
	}}
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(source, store, c)
	for _, step := range []time.Duration{0, time.Minute, 2 * time.Minute} {
		c.now = t0.Add(step)
		meter.Sample(context.Background())
	}
	if len(store.appended) != 2 {
		t.Fatalf("batches = %d", len(store.appended))
	}
	var a *Sample
	for i := range store.appended[1] {
		if store.appended[1][i].AppID == "x" {
			a = &store.appended[1][i]
		}
	}
	if a == nil || a.RxBps != 100 {
		t.Fatalf("a is rated over the 120 s since its kept baseline: %+v", a)
	}
}

type flakyRound struct {
	readings []Reading
	err      error
}

type flakySource struct {
	rounds []flakyRound
	calls  int
}

func (f *flakySource) Read(context.Context) ([]Reading, error) {
	round := f.rounds[f.calls]
	f.calls++
	return round.readings, round.err
}

type fixedOwners struct {
	owners map[string]Owner
	err    error
}

func (f fixedOwners) ProjectOwners(context.Context) (map[string]Owner, error) { return f.owners, f.err }

func twoRounds(project string) *scriptedSource {
	one := func(at time.Time, tx uint64) []Reading {
		return []Reading{{Key: "a", ProjectID: project, AppID: "x", At: at, Tx: tx}}
	}
	return &scriptedSource{rounds: [][]Reading{one(t0, 0), one(t0.Add(time.Minute), 6000)}}
}

func sampleTwice(meter *Meter, c *clock) {
	meter.Sample(context.Background())
	c.now = t0.Add(time.Minute)
	meter.Sample(context.Background())
}

// The owner and tier are stamped on the minute when it is taken, so the row
// keeps them after the project is deleted.
func TestMeter_StampsEachSampleWithTheOwnerAndTierOfItsProject(t *testing.T) {
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(twoRounds("p"), store, c).WithOwners(fixedOwners{owners: map[string]Owner{"p": {ID: "u1", Tier: "Free"}, "q": {ID: "u2", Tier: "standard"}}})
	sampleTwice(meter, c)
	got := store.appended[0][0]
	if got.OwnerID != "u1" || got.Tier != "free" {
		t.Fatalf("sample = %+v", got)
	}
}

func TestMeter_AProjectTheLookupDoesNotKnowIsStoredWithoutAnOwner(t *testing.T) {
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(twoRounds("p"), store, c).WithOwners(fixedOwners{owners: map[string]Owner{"other": {ID: "u1", Tier: "free"}}})
	sampleTwice(meter, c)
	if got := store.appended[0][0]; got.OwnerID != "" || got.Tier != "" || got.TxBps != 100 {
		t.Fatalf("the sample is still stored: %+v", got)
	}
}

func TestMeter_ALookupFailureStillStoresTheSamples(t *testing.T) {
	store := &memoryStore{}
	c := &clock{now: t0}
	meter := newTestMeter(twoRounds("p"), store, c).WithOwners(fixedOwners{err: errors.New("db")})
	sampleTwice(meter, c)
	if len(store.appended) != 1 || store.appended[0][0].OwnerID != "" {
		t.Fatalf("stored = %+v", store.appended)
	}
}

func TestMeter_WithoutALookupSamplesCarryNoOwner(t *testing.T) {
	store := &memoryStore{}
	c := &clock{now: t0}
	sampleTwice(newTestMeter(twoRounds("p"), store, c), c)
	if got := store.appended[0][0]; got.OwnerID != "" || got.Tier != "" {
		t.Fatalf("sample = %+v", got)
	}
}
