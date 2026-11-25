package main

import (
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
)

// standingLock is the leadership claim a scheduler holds for as long as it
// leads. The Postgres store shares one database session across all of them;
// without that each claim pinned its own pool connection, and the schedulers
// alone outnumbered the pool (EXC-746).
func standingLock(sqlStore storage.PlatformStore, key int64) storage.LeaderLock {
	if shared, ok := sqlStore.(interface {
		LeaderLock(key int64) storage.LeaderLock
	}); ok {
		return shared.LeaderLock(key)
	}
	return pgstore.NewAdvisoryLock(sqlStore.DB(), key)
}
