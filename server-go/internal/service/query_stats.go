package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Slow queries (EXC-654): the project's own statements as pg_stat_statements
// or MySQL's statement digests count them, normalized so no literal a
// customer sent is shown.

// ErrQueryStatsSort refuses a sort other than total or mean.
var ErrQueryStatsSort = errors.New("sort must be total or mean")

// ErrQueryStatsOff is a reset on a database whose statistics are not on.
var ErrQueryStatsOff = errors.New("query statistics are not turned on for this database")

const (
	queryStatsDefaultRows = 50
	queryStatsMaxRows     = 100
	sortByTotal           = "total"
	sortByMean            = "mean"
	engineNamePostgres    = "postgresql"
	engineNameMySQL       = "mysql"
)

// SlowQuery is one normalized statement and what it cost.
type SlowQuery struct {
	Query       string  `json:"query"`
	Calls       int64   `json:"calls"`
	TotalTimeMs float64 `json:"totalTimeMs"`
	MeanTimeMs  float64 `json:"meanTimeMs"`
	Rows        int64   `json:"rows"`
}

// SlowQueryReport is the same shape for Postgres and MySQL.
type SlowQueryReport struct {
	Engine  string      `json:"engine"`
	Enabled bool        `json:"enabled"`
	Sort    string      `json:"sort"`
	Queries []SlowQuery `json:"queries"`
}

// queryStatsDatabases reaches a Postgres project's primary as the superuser
// and turns statistics on; ProvisioningService is it.
type queryStatsDatabases interface {
	primaryPsql(ctx context.Context, inst *domain.DatabaseInstance) (roleSQLExecer, error)
	EnableQueryStats(ctx context.Context, projectID string) error
}

// MySQLStatsOpener opens a MySQL project as the platform login granted its digests.
type MySQLStatsOpener interface {
	OpenMySQLStats(ctx context.Context, projectID string) (*sql.DB, error)
}

type QueryStatsService struct {
	instances storage.InstanceStore
	databases queryStatsDatabases
	mysql     MySQLStatsOpener
	flags     features.Flags
}

func NewQueryStatsService(instances storage.InstanceStore, databases queryStatsDatabases, mysql MySQLStatsOpener, flags features.Flags) *QueryStatsService {
	return &QueryStatsService{instances: instances, databases: databases, mysql: mysql, flags: flags}
}

// List answers the project's statements, the costliest first by sort.
func (s *QueryStatsService) List(ctx context.Context, projectID, sort string, limit int) (*SlowQueryReport, error) {
	sort, err := querySort(sort)
	if err != nil {
		return nil, err
	}
	limit = queryRows(limit)
	inst, err := s.project(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if inst.DBType == domain.MySQL {
		return s.listMySQL(ctx, inst, sort, limit)
	}
	return s.listPostgres(ctx, inst, sort, limit)
}

// Reset clears the project's statistics.
func (s *QueryStatsService) Reset(ctx context.Context, projectID string) error {
	inst, err := s.project(ctx, projectID)
	if err != nil {
		return err
	}
	if inst.DBType == domain.MySQL {
		return s.resetMySQL(ctx, inst)
	}
	return s.resetPostgres(ctx, inst)
}

// Enable turns statistics on for a database created before the feature was;
// on Postgres it restarts the database once.
func (s *QueryStatsService) Enable(ctx context.Context, projectID string) error {
	if _, err := s.project(ctx, projectID); err != nil {
		return err
	}
	return s.databases.EnableQueryStats(ctx, projectID)
}

// project is the running project, refused for MySQL while that feature is off.
func (s *QueryStatsService) project(ctx context.Context, projectID string) (*domain.DatabaseInstance, error) {
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return nil, fmt.Errorf("read project %s: %w", projectID, err)
	}
	if inst == nil {
		return nil, fmt.Errorf(errProjectNotFoundFmt, projectID)
	}
	if inst.DBType == domain.MySQL && !features.Enabled(ctx, s.flags, features.MySQL) {
		return nil, ErrNotForMySQL
	}
	if !domain.IsActive(inst.Status) {
		return nil, fmt.Errorf("%w: %s is %s", ErrProjectNotActive, projectID, inst.Status)
	}
	return inst, nil
}

func querySort(sort string) (string, error) {
	switch sort {
	case "", sortByTotal:
		return sortByTotal, nil
	case sortByMean:
		return sortByMean, nil
	}
	return "", ErrQueryStatsSort
}

func queryRows(limit int) int {
	if limit <= 0 {
		return queryStatsDefaultRows
	}
	return min(limit, queryStatsMaxRows)
}

func emptyReport(engine, sort string) *SlowQueryReport {
	return &SlowQueryReport{Engine: engine, Sort: sort, Queries: []SlowQuery{}}
}

var _ queryStatsDatabases = (*ProvisioningService)(nil)
