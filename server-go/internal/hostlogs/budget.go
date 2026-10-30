package hostlogs

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// BudgetStore keeps each project's stored log text per UTC day (EXC-678), so
// a restart resumes the day's count instead of granting a second budget.
// Days are "2006-01-02".
type BudgetStore interface {
	HostLogBudgetUse(ctx context.Context, projectID, day string) (bytes int64, noticed bool, err error)
	AddHostLogBudgetUse(ctx context.Context, projectID, day string, bytes int64, noticed bool) error
	PruneHostLogBudget(ctx context.Context, before string) error
}

// budget is each project's stored log text per UTC day, so one project's
// flood cannot fill the host's disk.
type budget struct {
	store BudgetStore
	mu    sync.Mutex
	used  map[budgetKey]*dayUse
	// newest is the latest day seen; days two before it are forgotten.
	newest string
}

// budgetReadTimeout bounds the one store read per project and day.
const budgetReadTimeout = 2 * time.Second

type budgetKey struct{ project, day string }

type dayUse struct {
	bytes   int64
	noticed bool
}

// charge is what one batch took from a day: written once Loki stores it.
type charge struct {
	bytes   int64
	noticed bool
}

// newBudget counts in memory and, with a store, resumes from it.
func newBudget(store BudgetStore) *budget {
	return &budget{store: store, used: map[budgetKey]*dayUse{}}
}

// take answers whether size more bytes of the project's lines are stored on
// at's day under limit, and whether this is the day's first refusal, which
// earns a notice.
func (b *budget) take(ctx context.Context, projectID string, limit int64, at time.Time, size int64) (keep, notice bool) {
	use := b.entry(ctx, budgetKey{projectID, at.UTC().Format(time.DateOnly)})
	b.mu.Lock()
	defer b.mu.Unlock()
	if use.bytes+size <= limit {
		use.bytes += size
		return true, false
	}
	if use.noticed {
		return false, false
	}
	use.noticed = true
	return false, true
}

// entry is a project's day, read from the store the first time it is seen.
// A new latest day forgets days two before it; an older day being read stays.
func (b *budget) entry(ctx context.Context, key budgetKey) *dayUse {
	b.mu.Lock()
	use, known := b.used[key]
	b.mu.Unlock()
	if known {
		return use
	}
	stored := &dayUse{}
	if b.store != nil {
		readCtx, cancel := context.WithTimeout(ctx, budgetReadTimeout)
		bytes, noticed, err := b.store.HostLogBudgetUse(readCtx, key.project, key.day)
		cancel()
		if err != nil {
			// Counted from zero for this run; the next restart reads the table again.
			log.Printf("host logs: budget of %s on %s: %v", key.project, key.day, err)
		} else {
			stored.bytes, stored.noticed = bytes, noticed
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if raced, ok := b.used[key]; ok {
		return raced
	}
	b.used[key] = stored
	if key.day > b.newest {
		b.newest = key.day
		oldest := dayBefore(key.day, 2)
		for other := range b.used {
			if other.day < oldest {
				delete(b.used, other)
			}
		}
	}
	return stored
}

// refund gives back what a batch took but never stored.
func (b *budget) refund(projectID string, charges map[string]*charge) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for day, taken := range charges {
		use, ok := b.used[budgetKey{projectID, day}]
		if !ok {
			continue
		}
		use.bytes = max(use.bytes-taken.bytes, 0)
		if taken.noticed {
			use.noticed = false
		}
	}
}

// record writes what a stored batch took; nothing without a store.
func (b *budget) record(ctx context.Context, projectID string, charges map[string]*charge) error {
	if b.store == nil {
		return nil
	}
	var errs []error
	for day, taken := range charges {
		if taken.bytes == 0 && !taken.noticed {
			continue
		}
		errs = append(errs, b.store.AddHostLogBudgetUse(ctx, projectID, day, taken.bytes, taken.noticed))
	}
	return errors.Join(errs...)
}

// dayBefore is the day n days before day; day itself if it does not parse.
func dayBefore(day string, n int) string {
	at, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return day
	}
	return at.AddDate(0, 0, -n).Format(time.DateOnly)
}
