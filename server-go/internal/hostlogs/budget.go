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
}

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
	key := budgetKey{projectID, at.UTC().Format(time.DateOnly)}
	b.load(ctx, key)
	b.mu.Lock()
	defer b.mu.Unlock()
	use := b.used[key]
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

// load reads a project's day from the store the first time it is seen, and
// forgets days older than yesterday.
func (b *budget) load(ctx context.Context, key budgetKey) {
	b.mu.Lock()
	_, known := b.used[key]
	b.mu.Unlock()
	if known {
		return
	}
	stored := &dayUse{}
	if b.store != nil {
		bytes, noticed, err := b.store.HostLogBudgetUse(ctx, key.project, key.day)
		if err != nil {
			// Counted from zero for this run; the next restart reads the table again.
			log.Printf("host logs: budget of %s on %s: %v", key.project, key.day, err)
		} else {
			stored.bytes, stored.noticed = bytes, noticed
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, raced := b.used[key]; raced {
		return
	}
	b.used[key] = stored
	oldest := dayBefore(key.day, 1)
	for other := range b.used {
		if other.day < oldest {
			delete(b.used, other)
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
