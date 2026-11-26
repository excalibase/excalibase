package service

import (
	"slices"
	"sync"
	"time"
)

// stampMemory is how long a handed-out stamp is remembered. Two backups named
// for the same second can only meet while the clock is still in that second,
// so a minute is far more than the names need.
const stampMemory = time.Minute

// backupStamps hands out the second a manual backup is named for. Backups are
// named for their start to the second, so two started in the same second
// would share a name and the second would overwrite the first (EXC-516). A
// stamp is therefore never given twice for one project: a repeat moves on to
// the next second that is free.
type backupStamps struct {
	mu     sync.Mutex
	issued map[string][]time.Time
}

func (b *backupStamps) next(projectID string, now time.Time) time.Time {
	stamp := now.UTC().Truncate(time.Second)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.issued == nil {
		b.issued = map[string][]time.Time{}
	}
	recent := slices.DeleteFunc(b.issued[projectID], func(at time.Time) bool { return stamp.Sub(at) > stampMemory })
	for slices.ContainsFunc(recent, stamp.Equal) {
		stamp = stamp.Add(time.Second)
	}
	b.issued[projectID] = append(recent, stamp)
	return stamp
}
