package creditmeter

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// SampleStore takes the samples. A sample is stamped with the database clock.
type SampleStore interface {
	// SampledOrgs are the orgs that are metered.
	SampledOrgs(ctx context.Context) ([]string, error)
	// SampleOrg records what the org reserves now, under the org lock.
	SampleOrg(ctx context.Context, orgID string) error
}

// Sampler records, every five minutes, what each metered org reserves.
type Sampler struct{ store SampleStore }

func NewSampler(store SampleStore) *Sampler { return &Sampler{store: store} }

// Pass samples every metered org. One that fails is reported at the end and
// does not stop the others.
func (s *Sampler) Pass(ctx context.Context) error {
	orgs, err := s.store.SampledOrgs(ctx)
	if err != nil {
		return fmt.Errorf("list sampled orgs: %w", err)
	}
	var failures []error
	for _, orgID := range orgs {
		if err := s.store.SampleOrg(ctx, orgID); err != nil {
			failures = append(failures, fmt.Errorf("sample org %s: %w", orgID, err))
		}
	}
	return errors.Join(failures...)
}

// NextDelay is how long to wait for the next pass: until a random point inside
// the next five-minute slot, so nobody can tell when the sample is taken and
// stop a service just before it (EXC-866 R14).
func NextDelay(now time.Time) time.Duration {
	return nextDelay(now, rand.Int64N)
}

func nextDelay(now time.Time, offsetBelow func(n int64) int64) time.Duration {
	nextSlot := now.UTC().Truncate(SlotLength).Add(SlotLength)
	return nextSlot.Sub(now) + time.Duration(offsetBelow(int64(SlotLength)))
}
