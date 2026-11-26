package netmeter

import (
	"context"
	"log"
	"time"
)

const pruneEvery = time.Hour

// Store keeps the samples.
type Store interface {
	AppendNetSamples(ctx context.Context, samples []Sample) error
	// PruneNetSamples rolls finished hours up and drops what is past retention.
	PruneNetSamples(ctx context.Context, now time.Time) error
}

// Meter samples a Source once per tick and stores one row per app. Run it on
// the leading replica only.
type Meter struct {
	source   Source
	store    Store
	tracker  *Tracker
	interval time.Duration
	now      func() time.Time

	lastSample time.Time
	lastPrune  time.Time
}

func NewMeter(source Source, store Store, interval time.Duration) *Meter {
	return &Meter{source: source, store: store, tracker: NewTracker(), interval: interval, now: time.Now}
}

// Sample takes one round. A gap of more than three intervals since the last
// round means this replica was not sampling (a leader change, a stalled loop),
// so baselines are dropped and that round only records new ones: a rate over
// an hour would hide a burst.
func (m *Meter) Sample(ctx context.Context) {
	now := m.now()
	if !m.lastSample.IsZero() && now.Sub(m.lastSample) > 3*m.interval {
		m.tracker.Reset()
	}
	m.lastSample = now
	readings, err := m.source.Read(ctx)
	if err != nil {
		log.Printf("net meter: read: %v", err)
	}
	samples := Aggregate(now, m.tracker.Observe(readings))
	if len(samples) > 0 {
		if err := m.store.AppendNetSamples(ctx, samples); err != nil {
			log.Printf("net meter: store %d samples: %v", len(samples), err)
		}
	}
	m.pruneIfDue(ctx, now)
}

func (m *Meter) pruneIfDue(ctx context.Context, now time.Time) {
	if !m.lastPrune.IsZero() && now.Sub(m.lastPrune) < pruneEvery {
		return
	}
	m.lastPrune = now
	if err := m.store.PruneNetSamples(ctx, now); err != nil {
		log.Printf("net meter: prune: %v", err)
	}
}
