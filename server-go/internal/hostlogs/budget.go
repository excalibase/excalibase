package hostlogs

import (
	"sync"
	"time"
)

// budget is each project's stored log text per UTC day, so one project's
// flood cannot fill the host's disk.
type budget struct {
	limit int64
	mu    sync.Mutex
	used  map[string]*dayUse
}

type dayUse struct {
	day     string
	bytes   int64
	noticed bool
}

func newBudget(limit int64) *budget {
	return &budget{limit: limit, used: map[string]*dayUse{}}
}

// take answers whether size more bytes of the project's lines are stored on
// at's day, and whether this is the day's first refusal, which earns a notice.
func (b *budget) take(projectID string, at time.Time, size int64) (keep, notice bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	day := at.UTC().Format(time.DateOnly)
	use := b.used[projectID]
	if use == nil || use.day != day {
		use = &dayUse{day: day}
		b.used[projectID] = use
	}
	if use.bytes+size <= b.limit {
		use.bytes += size
		return true, false
	}
	if use.noticed {
		return false, false
	}
	use.noticed = true
	return false, true
}
