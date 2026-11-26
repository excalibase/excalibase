package netmeter

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
)

const pruneEvery = time.Hour

// Store keeps the samples.
type Store interface {
	AppendNetSamples(ctx context.Context, samples []Sample) error
	// PruneNetSamples rolls finished hours up and drops what is past retention.
	PruneNetSamples(ctx context.Context, now time.Time) error
}

// Owner is who owns a project and on what tier, as stamped on its samples.
type Owner struct{ ID, Tier string }

// Owners says who owns each project now. The meter stamps it on every sample
// so the history does not depend on the project still existing.
type Owners interface {
	ProjectOwners(ctx context.Context) (map[string]Owner, error)
}

// Meter samples a Source once per tick and stores one row per app. Run it on
// the leading replica only.
type Meter struct {
	source   Source
	store    Store
	tracker  *Tracker
	owners   Owners
	interval time.Duration
	now      func() time.Time

	lastSample time.Time
	lastPrune  time.Time
}

func NewMeter(source Source, store Store, interval time.Duration) *Meter {
	return &Meter{source: source, store: store, tracker: NewTracker(), interval: interval, now: time.Now}
}

// WithOwners makes the meter stamp each sample with its project's owner and tier.
func (m *Meter) WithOwners(owners Owners) *Meter {
	m.owners = owners
	return m
}

// StampsOwners says whether samples are stamped with their project's owner and tier.
func (m *Meter) StampsOwners() bool { return m.owners != nil }

// stamp adds the owner and lower-case tier of each sample's project. A failed
// lookup, or a project it does not know, leaves the sample unstamped: the
// minute is still worth keeping.
func (m *Meter) stamp(ctx context.Context, samples []Sample) {
	if m.owners == nil || len(samples) == 0 {
		return
	}
	owners, err := m.owners.ProjectOwners(ctx)
	if err != nil {
		log.Printf("net meter: owners: %v", err)
		return
	}
	for i := range samples {
		if owner, ok := owners[samples[i].ProjectID]; ok {
			samples[i].OwnerID, samples[i].Tier = owner.ID, strings.ToLower(owner.Tier)
		}
	}
}

// Sample takes one round. A gap of more than three intervals since the last
// round that read anything means this replica was not sampling (a leader
// change, a long outage), so baselines are dropped and that round only records
// new ones: a rate over an hour would hide a burst. A round that read nothing
// because the source failed leaves the baselines alone; a partial failure keeps
// the baselines of what the failed part would have covered.
func (m *Meter) Sample(ctx context.Context) {
	now := m.now()
	if !m.lastSample.IsZero() && now.Sub(m.lastSample) > 3*m.interval {
		m.tracker.Reset()
	}
	readings, err := m.source.Read(ctx)
	var unobserved []string
	if err != nil {
		log.Printf("net meter: read: %v", err)
		var partial *PartialError
		if errors.As(err, &partial) {
			unobserved = partial.Unobserved
		}
		if len(readings) == 0 {
			m.pruneIfDue(ctx, now)
			return
		}
	}
	m.lastSample = now
	samples := Aggregate(now, m.tracker.Observe(readings, unobserved...))
	if len(samples) > 0 {
		m.stamp(ctx, samples)
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
