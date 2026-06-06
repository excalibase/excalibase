package service

import (
	"context"
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
	const slowQuerySQL = `SELECT count(*) FROM pg_stat_statements WHERE mean_exec_time > 1000`
	const avgTimeSQL = `SELECT ROUND(avg(mean_exec_time)::numeric, 2) FROM pg_stat_statements WHERE calls > 0`

	summary.ActiveConnections = s.queryInt(ctx, inst, "SELECT count(*) FROM pg_stat_activity WHERE state='active'")
	summary.TotalConnections = s.queryInt(ctx, inst, "SELECT count(*) FROM pg_stat_activity")
	summary.CacheHitRatio = s.queryFloat(ctx, inst, cacheHitSQL)
	summary.SlowQueryCount = s.queryInt(ctx, inst, slowQuerySQL)
	summary.AvgQueryTimeMs = s.queryFloat(ctx, inst, avgTimeSQL)

	if out, err := s.execSQL(ctx, inst, `SELECT pg_size_pretty(pg_database_size(current_database()))`); err == nil {
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

func (s *PerformanceService) GetTopQueries(ctx context.Context, projectID string, limit int) ([]domain.QueryStat, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf(errProjectNotFoundFmt, projectID)
	}

	sql := fmt.Sprintf(`SELECT query, calls, total_exec_time, mean_exec_time, min_exec_time, max_exec_time, rows FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT %d`, limit)
	out, err := s.execSQL(ctx, inst, sql)
	if err != nil {
		return nil, err
	}

	return parseQueryStats(out), nil
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

func parseQueryStats(out string) []domain.QueryStat {
	var stats []domain.QueryStat
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) < 7 {
			continue
		}
		calls, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		totalTime, _ := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
		meanTime, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
		minTime, _ := strconv.ParseFloat(strings.TrimSpace(parts[4]), 64)
		maxTime, _ := strconv.ParseFloat(strings.TrimSpace(parts[5]), 64)
		rows, _ := strconv.ParseInt(strings.TrimSpace(parts[6]), 10, 64)

		stats = append(stats, domain.QueryStat{
			Query:           strings.TrimSpace(parts[0]),
			Calls:           calls,
			TotalExecTimeMs: totalTime,
			AvgExecTimeMs:   meanTime,
			MinExecTimeMs:   minTime,
			MaxExecTimeMs:   maxTime,
			Rows:            rows,
		})
	}
	return stats
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
