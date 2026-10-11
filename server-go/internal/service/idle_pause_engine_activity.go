package service

import (
	"context"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Apps reach some engines directly (MySQL, ADR 0041), so the platform's API
// never sees their use. For those the sweep reads the work the database's
// clients did, a counter that only grows while mysqld runs, and compares it
// with the reading it stored at the previous sweep (EXC-588).

// EngineActivityReader reads the work a project's clients have done since
// its database started: statements plus connections, platform users excluded.
type EngineActivityReader interface {
	ClientWork(ctx context.Context, inst *domain.DatabaseInstance) (int64, error)
}

// DatabaseWorkCounters keeps the last reading per project.
type DatabaseWorkCounters = storage.DatabaseWorkStore

// MySQLEngineActivity reads a MySQL project's client work from its cluster.
type MySQLEngineActivity struct{ mysql *provisioner.MySQLProvisioner }

// NewMySQLEngineActivity reads through the MOCO provisioner.
func NewMySQLEngineActivity(mysql *provisioner.MySQLProvisioner) *MySQLEngineActivity {
	return &MySQLEngineActivity{mysql: mysql}
}

// ClientWork is the project's client work summed over its instances.
func (m *MySQLEngineActivity) ClientWork(ctx context.Context, inst *domain.DatabaseInstance) (int64, error) {
	return m.mysql.ClientWork(ctx, inst.Namespace, inst.ProjectID)
}

type engineActivitySource struct {
	reader   EngineActivityReader
	counters DatabaseWorkCounters
}

// SetEngineActivity makes the sweep judge an engine's projects by their
// database's client work. An engine listed in noPlatformSignal without a
// reader is never paused for idleness.
func (s *IdlePauseScheduler) SetEngineActivity(engine domain.DatabaseType, reader EngineActivityReader, counters DatabaseWorkCounters) {
	if s.engineActivity == nil {
		s.engineActivity = map[domain.DatabaseType]engineActivitySource{}
	}
	s.engineActivity[engine] = engineActivitySource{reader: reader, counters: counters}
}

// noPlatformSignal are the engines whose use the platform API does not see.
var noPlatformSignal = map[domain.DatabaseType]bool{domain.MySQL: true}

// withEngineActivity folds the database's own reading into the project's
// activity. False means there is no signal to judge the project by.
func (s *IdlePauseScheduler) withEngineActivity(ctx context.Context, inst *domain.DatabaseInstance, row domain.ProjectActivity, seen bool, now time.Time) (domain.ProjectActivity, bool, bool) {
	source, wired := s.engineActivity[inst.DBType]
	if !wired {
		return row, seen, !noPlatformSignal[inst.DBType]
	}
	work, err := source.reader.ClientWork(ctx, inst)
	if err != nil {
		s.logger.Printf("idle-pause: read the client work of %s: %v", inst.ProjectID, err)
		return row, seen, false
	}
	previous, known, err := source.counters.DatabaseWork(ctx, inst.ProjectID)
	if err != nil {
		s.logger.Printf("idle-pause: last client work of %s: %v", inst.ProjectID, err)
		return row, seen, false
	}
	if known && previous == work {
		return row, seen, true
	}
	// More work, a restart (the counter went back) or a first reading. The
	// reading is stored only once recorded as activity, so none is lost.
	seenBy := domain.ActivitySourceDatabase.String()
	if err := s.activity.TouchProjectActivity(ctx, inst.ProjectID, seenBy, now); err != nil {
		s.logger.Printf("idle-pause: record database activity of %s: %v", inst.ProjectID, err)
		return row, seen, false
	}
	if err := source.counters.SaveDatabaseWork(ctx, inst.ProjectID, work, now); err != nil {
		s.logger.Printf("idle-pause: store the client work of %s: %v", inst.ProjectID, err)
	}
	return domain.ProjectActivity{ProjectID: inst.ProjectID, LastSeenAt: now, LastSeenSource: seenBy}, true, true
}
