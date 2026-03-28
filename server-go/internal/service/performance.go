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
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	summary := &domain.PerformanceSummary{Available: true}

	// Check if pg_stat_statements is available
	output, err := s.execSQL(ctx, inst, "SELECT 1 FROM pg_extension WHERE extname='pg_stat_statements'")
	if err != nil || !strings.Contains(output, "1") {
		summary.Available = false
		summary.UnavailableReason = "pg_stat_statements extension is not enabled"
		return summary, nil
	}

	// Active connections
	out, err := s.execSQL(ctx, inst, "SELECT count(*) FROM pg_stat_activity WHERE state='active'")
	if err == nil {
		if v, err := parseFirstInt(out); err == nil {
			summary.ActiveConnections = &v
		}
	}

	// Total connections
	out, err = s.execSQL(ctx, inst, "SELECT count(*) FROM pg_stat_activity")
	if err == nil {
		if v, err := parseFirstInt(out); err == nil {
			summary.TotalConnections = &v
		}
	}

	// Cache hit ratio
	out, err = s.execSQL(ctx, inst, `SELECT ROUND(sum(blks_hit)*100.0/NULLIF(sum(blks_hit)+sum(blks_read),0),2) FROM pg_stat_database`)
	if err == nil {
		if v, err := parseFirstFloat(out); err == nil {
			summary.CacheHitRatio = &v
		}
	}

	// Database size
	out, err = s.execSQL(ctx, inst, `SELECT pg_size_pretty(pg_database_size(current_database()))`)
	if err == nil {
		summary.DatabaseSize = strings.TrimSpace(extractFirstLine(out))
	}

	// Slow queries (> 1s avg)
	out, err = s.execSQL(ctx, inst, `SELECT count(*) FROM pg_stat_statements WHERE mean_exec_time > 1000`)
	if err == nil {
		if v, err := parseFirstInt(out); err == nil {
			summary.SlowQueryCount = &v
		}
	}

	// Average query time
	out, err = s.execSQL(ctx, inst, `SELECT ROUND(avg(mean_exec_time)::numeric, 2) FROM pg_stat_statements WHERE calls > 0`)
	if err == nil {
		if v, err := parseFirstFloat(out); err == nil {
			summary.AvgQueryTimeMs = &v
		}
	}

	return summary, nil
}

func (s *PerformanceService) GetTopQueries(ctx context.Context, projectID string, limit int) ([]domain.QueryStat, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
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
		return nil, fmt.Errorf("project not found: %s", projectID)
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
