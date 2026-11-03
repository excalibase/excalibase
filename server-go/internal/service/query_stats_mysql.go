package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// MySQL's digests carry no user, so they are scoped by the default database
// the statement ran in. DIGEST_TEXT is normalized; QUERY_SAMPLE_TEXT keeps
// literals and is never read. The platform's own read of the table is left out.
const mysqlDigestsSQL = `SELECT DIGEST_TEXT, COUNT_STAR, SUM_TIMER_WAIT, SUM_ROWS_SENT + SUM_ROWS_AFFECTED
FROM performance_schema.events_statements_summary_by_digest
WHERE SCHEMA_NAME = ? AND DIGEST_TEXT IS NOT NULL AND COUNT_STAR > 0
  AND DIGEST_TEXT NOT LIKE '%performance_schema%'
ORDER BY CASE WHEN ? = 'mean' THEN SUM_TIMER_WAIT / COUNT_STAR ELSE SUM_TIMER_WAIT END DESC LIMIT ?`

// mysqlResetDigestsSQL clears every digest of the project's own server.
const mysqlResetDigestsSQL = "TRUNCATE TABLE performance_schema.events_statements_summary_by_digest"

// mysqlAccessDenied is ER_TABLEACCESS_DENIED_ERROR: the grant is not there yet.
const mysqlAccessDenied = 1142

// picosPerMilli converts MySQL's picosecond timers.
const picosPerMilli = 1e9

func (s *QueryStatsService) listMySQL(ctx context.Context, inst *domain.DatabaseInstance, sort string, limit int) (*SlowQueryReport, error) {
	report := emptyReport(engineNameMySQL, sort)
	db, err := s.mysqlStats(ctx, inst)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, mysqlDigestsSQL, inst.DatabaseName, sort, limit)
	if isMySQLDenied(err) {
		return report, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read query statistics of %s: %w", inst.ProjectID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var query SlowQuery
		var picos float64
		if err := rows.Scan(&query.Query, &query.Calls, &picos, &query.Rows); err != nil {
			return nil, fmt.Errorf("read query statistics of %s: %w", inst.ProjectID, err)
		}
		query.TotalTimeMs = picos / picosPerMilli
		query.MeanTimeMs = query.TotalTimeMs / float64(query.Calls)
		report.Queries = append(report.Queries, query)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read query statistics of %s: %w", inst.ProjectID, err)
	}
	report.Enabled = true
	return report, nil
}

func (s *QueryStatsService) resetMySQL(ctx context.Context, inst *domain.DatabaseInstance) error {
	db, err := s.mysqlStats(ctx, inst)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, mysqlResetDigestsSQL)
	if isMySQLDenied(err) {
		return ErrQueryStatsOff
	}
	if err != nil {
		return fmt.Errorf("reset query statistics of %s: %w", inst.ProjectID, err)
	}
	return nil
}

func (s *QueryStatsService) mysqlStats(ctx context.Context, inst *domain.DatabaseInstance) (*sql.DB, error) {
	if s.mysql == nil {
		return nil, errors.New("no MySQL project pools configured")
	}
	db, err := s.mysql.OpenMySQLStats(ctx, inst.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", inst.ProjectID, err)
	}
	return db, nil
}

func isMySQLDenied(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == mysqlAccessDenied
}
