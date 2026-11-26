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
