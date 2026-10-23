package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type PerformanceService struct {
	store     storage.InstanceStore
	k8sClient k8s.KubeClient
}

func NewPerformanceService(store storage.InstanceStore, client k8s.KubeClient) *PerformanceService {
	return &PerformanceService{store: store, k8sClient: client}
}

func (s *PerformanceService) GetSummary(ctx context.Context, projectID string) (*domain.PerformanceSummary, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf(errProjectNotFoundFmt, projectID)
	}

	summary := &domain.PerformanceSummary{Available: true}

	// Check if pg_stat_statements is available
	output, err := s.execSQL(ctx, inst, "SELECT 1 FROM pg_extension WHERE extname='pg_stat_statements'")
	if err != nil || !strings.Contains(output, "1") {
		summary.Available = false
		summary.UnavailableReason = "pg_stat_statements extension is not enabled"
		return summary, nil
	}

	s.populateSummaryMetrics(ctx, inst, summary)
	return summary, nil
}

// populateSummaryMetrics fills summary fields by querying pg_stat_activity and pg_stat_statements.
// Each metric is best-effort: a query error leaves the field nil/zero.
func (s *PerformanceService) populateSummaryMetrics(ctx context.Context, inst *domain.DatabaseInstance, summary *domain.PerformanceSummary) {
	const cacheHitSQL = `SELECT ROUND(sum(blks_hit)*100.0/NULLIF(sum(blks_hit)+sum(blks_read),0),2) FROM pg_stat_database`

	summary.ActiveConnections = s.queryInt(ctx, inst, "SELECT count(*) FROM pg_stat_activity WHERE state='active'")
	summary.TotalConnections = s.queryInt(ctx, inst, "SELECT count(*) FROM pg_stat_activity")
	summary.CacheHitRatio = s.queryFloat(ctx, inst, cacheHitSQL)
	s.summarizeStatements(ctx, inst, summary)

	if inst.DatabaseName == "" {
		return
	}
	if out, err := s.execSQL(ctx, inst, databaseSizeSQL(inst)); err == nil {
		summary.DatabaseSize = strings.TrimSpace(extractFirstLine(out))
	}
}

// queryInt runs a single-cell SQL query and returns the int result, or nil on any error.
func (s *PerformanceService) queryInt(ctx context.Context, inst *domain.DatabaseInstance, sql string) *int {
	out, err := s.execSQL(ctx, inst, sql)
	if err != nil {
		return nil
	}
	v64, err := parseFirstInt(out)
	if err != nil {
		return nil
	}
	v := int(v64)
	return &v
}

// queryFloat runs a single-cell SQL query and returns the float64 result, or nil on any error.
func (s *PerformanceService) queryFloat(ctx context.Context, inst *domain.DatabaseInstance, sql string) *float64 {
	out, err := s.execSQL(ctx, inst, sql)
	if err != nil {
		return nil
	}
	v, err := parseFirstFloat(out)
	if err != nil {
		return nil
	}
	return &v
}

// GetTopQueries is the project's own statements, read as the slow-queries
// page reads them (EXC-654).
func (s *PerformanceService) GetTopQueries(ctx context.Context, projectID string, limit int) ([]domain.QueryStat, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf(errProjectNotFoundFmt, projectID)
	}
	statements, err := s.projectStatements(ctx, inst, queryRows(limit))
	if err != nil {
		return nil, err
	}
	stats := make([]domain.QueryStat, 0, len(statements))
	for _, q := range statements {
		stats = append(stats, domain.QueryStat{Query: q.Query, Calls: q.Calls, TotalExecTimeMs: q.TotalTimeMs, AvgExecTimeMs: q.MeanTimeMs, Rows: q.Rows})
	}
	return stats, nil
}

// slowStatementMs is when the summary counts a statement as slow.
const slowStatementMs = 1000

// summarizeStatements counts the slow ones among the project's costliest statements.
func (s *PerformanceService) summarizeStatements(ctx context.Context, inst *domain.DatabaseInstance, summary *domain.PerformanceSummary) {
	statements, err := s.projectStatements(ctx, inst, queryStatsMaxRows)
	if err != nil {
		return
	}
	slow, sum := 0, 0.0
	for _, q := range statements {
		if q.MeanTimeMs > slowStatementMs {
			slow++
		}
		sum += q.MeanTimeMs
	}
	summary.SlowQueryCount = &slow
	if len(statements) > 0 {
		avg := sum / float64(len(statements))
		summary.AvgQueryTimeMs = &avg
	}
}

func (s *PerformanceService) projectStatements(ctx context.Context, inst *domain.DatabaseInstance, limit int) ([]SlowQuery, error) {
	out, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, inst.ProjectID+"-postgres-1", "postgres",
		queryStatsPsql(inst.DatabaseName, queryStatsScript(sortByTotal, limit)))
	if err != nil {
		return nil, err
	}
	statements := []SlowQuery{}
	if answer := lastLine(out); answer != "" {
		if err := json.Unmarshal([]byte(answer), &statements); err != nil {
			return nil, fmt.Errorf("read query statistics of %s: unexpected answer", inst.ProjectID)
		}
	}
	return statements, nil
}

func (s *PerformanceService) GetWaitEvents(ctx context.Context, projectID string) ([]domain.WaitEvent, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf(errProjectNotFoundFmt, projectID)
	}

	sql := `SELECT wait_event_type, wait_event, count(*) FROM pg_stat_activity WHERE wait_event IS NOT NULL GROUP BY wait_event_type, wait_event ORDER BY count DESC`
	out, err := s.execSQL(ctx, inst, sql)
	if err != nil {
		return nil, err
	}

	return parseWaitEvents(out), nil
}

func (s *PerformanceService) execSQL(ctx context.Context, inst *domain.DatabaseInstance, sql string) (string, error) {
	pod := inst.ProjectID + "-postgres-1"
	return s.k8sClient.ExecInPod(ctx, inst.Namespace, pod, "postgres",
		[]string{"psql", "-U", "postgres", "-t", "-A", "-c", sql})
}

func extractFirstLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return ""
}

func parseFirstInt(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(extractFirstLine(s)))
}

func parseFirstFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(extractFirstLine(s)), 64)
}

func parseWaitEvents(out string) []domain.WaitEvent {
	var events []domain.WaitEvent
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		count, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		events = append(events, domain.WaitEvent{
			WaitEventType: strings.TrimSpace(parts[0]),
			WaitEvent:     strings.TrimSpace(parts[1]),
			Count:         count,
		})
	}
	return events
}

// databaseSizeSQL sizes the customer's data: the project database, plus for
// a DocumentDB project the postgres database its documents live in. psql
// here connects to postgres, so current_database() would size the wrong one.
func databaseSizeSQL(inst *domain.DatabaseInstance) string {
	size := "pg_database_size(" + sqlTextLiteral(inst.DatabaseName) + ")"
	if inst.DocumentDB {
		size += " + pg_database_size('postgres')"
	}
	return "SELECT pg_size_pretty(" + size + ")"
}
