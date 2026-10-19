package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const metricsHistoryFile = "metrics-history.json"

type MetricsService struct {
	store       storage.InstanceStore
	k8sClient   k8s.KubeClient
	storagePath string
	mu          sync.RWMutex
	history     map[string][]domain.DatabaseMetrics
	// table, when set, keeps history in place of the file (SetHistory).
	table      MetricsHistory
	alerts     *AlertingService
	tierLimits TierLimits
}

func NewMetricsService(store storage.InstanceStore, client k8s.KubeClient, storagePath string) *MetricsService {
	return &MetricsService{
		store:       store,
		k8sClient:   client,
		storagePath: storagePath,
		history:     make(map[string][]domain.DatabaseMetrics),
	}
}

func (s *MetricsService) GetCurrentMetrics(ctx context.Context, projectID string) (*domain.DatabaseMetrics, error) {
	inst, err := s.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}

	metrics := s.collectMetrics(ctx, inst)
	if s.table != nil {
		return metrics, nil
	}

	// Append to history
	s.mu.Lock()
	s.history[projectID] = append(s.history[projectID], *metrics)
	if len(s.history[projectID]) > 100 {
		s.history[projectID] = s.history[projectID][len(s.history[projectID])-100:]
	}
	s.mu.Unlock()

	// Persist to disk
	s.saveHistory(projectID)

	return metrics, nil
}

func (s *MetricsService) GetMetricsHistory(ctx context.Context, projectID string, limit int) (*domain.MetricsHistory, error) {
	if s.table != nil {
		return s.tableHistory(ctx, projectID, limit)
	}
	s.mu.RLock()
	hist := s.history[projectID]
	s.mu.RUnlock()

	if hist == nil {
		hist = s.loadHistory(projectID)
		s.mu.Lock()
		s.history[projectID] = hist
		s.mu.Unlock()
	}

	if limit > 0 && len(hist) > limit {
		hist = hist[len(hist)-limit:]
	}

	return &domain.MetricsHistory{
		ProjectID:   projectID,
		Metrics:     hist,
		TotalPoints: len(hist),
	}, nil
}

func (s *MetricsService) collectMetrics(ctx context.Context, inst *domain.DatabaseInstance) *domain.DatabaseMetrics {
	now := &domain.FlexTime{Time: time.Now()}
	metrics := &domain.DatabaseMetrics{
		ProjectID:        inst.ProjectID,
		Timestamp:        now,
		Status:           inst.Status,
		MetricsAvailable: true,
	}

	if s.k8sClient == nil {
		metrics.MetricsAvailable = false
		reason := "database metrics are read from Kubernetes; this installation runs its databases on a single host"
		metrics.UnavailableReason = &reason
		return metrics
	}
	tierInstances := tierInstanceCount(inst.Tier)
	if inst.DBType == domain.MySQL {
		return s.collectMySQLMetrics(ctx, inst, tierInstances, metrics)
	}

	// Collect CNPG Prometheus metrics
	if err := s.collectCNPGMetrics(ctx, inst, tierInstances, metrics); err != nil {
		metrics.MetricsAvailable = false
		reason := err.Error()
		metrics.UnavailableReason = &reason
		return metrics
	}

	metrics.HealthStatus = healthOf(inst)

	storageLimit := parseStorage(inst.Tier)
	if inst.StorageSize != "" {
		storageLimit = inst.StorageSize
	}
	metrics.StorageLimit = &storageLimit
	metrics.InstanceCount = &tierInstances
	s.collectDiskUsage(ctx, inst, postgresVolume(inst), metrics)

	limits := s.databaseLimits(ctx, inst)
	if total, instances, ok := limits.total(); ok {
		metrics.CPULimitCores = &total.cpuCores
		metrics.MemoryLimitMB = &total.memoryMB
		metrics.InstanceCount = &instances
	}
	s.collectPodResourceMetrics(ctx, inst, limits, metrics)

	return metrics
}

// collectMySQLMetrics reports what a MySQL project has without CNPG's
// exporter: its health, plan limits and data volume.
func (s *MetricsService) collectMySQLMetrics(ctx context.Context, inst *domain.DatabaseInstance, tierInstances int, metrics *domain.DatabaseMetrics) *domain.DatabaseMetrics {
	metrics.HealthStatus = healthOf(inst)
	storageLimit := parseStorage(inst.Tier)
	if inst.StorageSize != "" {
		storageLimit = inst.StorageSize
	}
	metrics.StorageLimit = &storageLimit
	metrics.InstanceCount = &tierInstances
	s.collectDiskUsage(ctx, inst, mysqlVolume(inst), metrics)
	if metrics.DiskUsagePercent == nil {
		metrics.MetricsAvailable = false
		reason := "the MySQL data volume could not be read"
		metrics.UnavailableReason = &reason
	}
	return metrics
}

func healthOf(inst *domain.DatabaseInstance) string {
	if inst.Status == "FAILED" {
		return "DOWN"
	}
	return "HEALTHY"
}

// collectCNPGMetrics fetches and parses CNPG Prometheus metrics from the primary pod,
// aggregating replica metrics for multi-instance tiers.
func (s *MetricsService) collectCNPGMetrics(ctx context.Context, inst *domain.DatabaseInstance, tierInstances int, metrics *domain.DatabaseMetrics) error {
	raw, err := s.fetchCNPGMetrics(ctx, inst.Namespace, inst.ProjectID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("prometheus exporter returned empty data")
	}

	labeled := parseLabeledMetrics(raw)

	// Connections (sum across all pods for multi-instance)
	totalActive := sumMetric(labeled, "cnpg_backends_total")
	if tierInstances > 1 {
		for i := 2; i <= tierInstances; i++ {
			pod := fmt.Sprintf("%s-postgres-%d", inst.ProjectID, i)
			replicaRaw, err := s.execMetricsFetch(ctx, inst.Namespace, pod)
			if err == nil {
				totalActive += sumMetric(parseLabeledMetrics(replicaRaw), "cnpg_backends_total")
			}
		}
	}
	metrics.ActiveConnections = intPtr(totalActive)

	// Idle connections
	metrics.IdleConnections = intPtr(sumMetricFiltered(labeled, "cnpg_backends_total", "\"idle\""))

	// Max connections
	if v, ok := labeled["cnpg_pg_settings_setting{name=\"max_connections\"}"]; ok {
		metrics.MaxConnections = intPtr(int(v))
	} else {
		metrics.MaxConnections = intPtr(100)
	}

	if size, ok := databaseSizeBytes(labeled, inst.DatabaseName, inst.DocumentDB); ok {
		gb := size / (1024 * 1024 * 1024)
		metrics.DatabaseSizeBytes = &size
		metrics.DatabaseSizeGB = &gb
	}

	// Last backup time
	if v, ok := labeled["cnpg_collector_last_available_backup_timestamp"]; ok && v > 0 {
		t := &domain.FlexTime{Time: time.Unix(int64(v), 0).UTC()}
		metrics.LastBackupTime = t
	}

	return nil
}

// collectPodResourceMetrics fetches CPU/memory usage from metrics-server and populates
// per-pod metrics and aggregate usage percentages.
// The namespace also runs the project's apps, functions and watcher, so only
// the database's own pods count.
func (s *MetricsService) collectPodResourceMetrics(ctx context.Context, inst *domain.DatabaseInstance, limits databaseLimits, metrics *domain.DatabaseMetrics) {
	podMetrics, err := s.k8sClient.GetPodMetrics(ctx, inst.Namespace)
	if err != nil {
		log.Printf("metrics of %s: pod usage: %v", inst.ProjectID, err)
		return
	}

	var usage, limit podLimit
	var counted int
	limited := true
	for _, pm := range podMetrics {
		if !strings.HasPrefix(pm.Name, inst.ProjectID+postgresSuffix+"-") {
			continue
		}
		counted++
		role := "replica"
		if strings.HasSuffix(pm.Name, "-1") {
			role = "primary"
		}
		own, ok := limits.of(pm.Name)
		limited = limited && ok
		metrics.Pods = append(metrics.Pods, domain.PodMetrics{
			Name:          pm.Name,
			Role:          role,
			CPUCores:      float64(pm.CPUMillis) / 1000.0,
			CPULimitCores: own.cpuCores,
			MemoryMB:      pm.MemoryMB,
			MemoryLimitMB: own.memoryMB,
		})
		usage.cpuCores += float64(pm.CPUMillis) / 1000.0
		usage.memoryMB += pm.MemoryMB
		limit.cpuCores += own.cpuCores
		limit.memoryMB += own.memoryMB
	}
	if counted == 0 {
		return
	}

	metrics.CPUUsageCores = &usage.cpuCores
	metrics.MemoryUsageMB = &usage.memoryMB
	// A share needs every sampled pod's limit; a guessed one would misstate it.
	if !limited || limit.cpuCores <= 0 || limit.memoryMB <= 0 {
		return
	}
	cpuPct := usage.cpuCores / limit.cpuCores * 100
	memPct := float64(usage.memoryMB) / float64(limit.memoryMB) * 100
	metrics.CPUUsagePercent = &cpuPct
	metrics.MemoryUsagePercent = &memPct
}

func (s *MetricsService) fetchCNPGMetrics(ctx context.Context, namespace, projectID string) (string, error) {
	pod := projectID + "-postgres-1"
	return s.execMetricsFetch(ctx, namespace, pod)
}

// exporterRequest reads CNPG's exporter from inside the postgres container.
// The tenant image is CNPG's standard image, which has bash but no python or
// curl, so the request goes over bash's /dev/tcp.
const exporterRequest = `exec 3<>/dev/tcp/localhost/9187 && printf 'GET /metrics HTTP/1.0\r\nHost: localhost\r\n\r\n' >&3 && cat <&3`

func (s *MetricsService) execMetricsFetch(ctx context.Context, namespace, pod string) (string, error) {
	response, err := s.k8sClient.ExecInPod(ctx, namespace, pod, "postgres", []string{"bash", "-c", exporterRequest})
	if err != nil {
		return "", err
	}
	return exporterBody(response)
}

// exporterBody is the metrics text of an HTTP/1.0 answer, or an error for
// anything but a 200.
func exporterBody(response string) (string, error) {
	head, body, found := strings.Cut(response, "\r\n\r\n")
	status, _, _ := strings.Cut(head, "\r\n")
	fields := strings.Fields(status)
	if !found || len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return "", fmt.Errorf("the metrics exporter gave no HTTP answer")
	}
	if fields[1] != "200" {
		return "", fmt.Errorf("the metrics exporter answered %s", strings.Join(fields[1:], " "))
	}
	return body, nil
}

func parseLabeledMetrics(raw string) map[string]float64 {
	result := make(map[string]float64)
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		val, err := strconv.ParseFloat(parts[len(parts)-1], 64)
		if err != nil {
			continue
		}
		result[parts[0]] = val
	}
	return result
}

func sumMetric(labeled map[string]float64, prefix string) int {
	var total int
	for k, v := range labeled {
		if strings.HasPrefix(k, prefix) {
			total += int(v)
		}
	}
	return total
}

func sumMetricFiltered(labeled map[string]float64, prefix, filter string) int {
	var total int
	for k, v := range labeled {
		if strings.HasPrefix(k, prefix) && strings.Contains(k, filter) {
			total += int(v)
		}
	}
	return total
}

func tierInstanceCount(tier domain.TierType) int {
	switch tier {
	case domain.Standard:
		return 3
	case domain.Enterprise:
		return 5
	default:
		return 1
	}
}

func parseStorage(tier domain.TierType) string {
	switch tier {
	case domain.Standard:
		return "50Gi"
	case domain.Enterprise:
		return "500Gi"
	default:
		return "5Gi"
	}
}

func (s *MetricsService) saveHistory(projectID string) {
	s.mu.RLock()
	hist := s.history[projectID]
	s.mu.RUnlock()

	dir := filepath.Join(s.storagePath, "projects", projectID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("WARN: mkdir %s: %v", dir, err)
		return
	}
	data, err := json.Marshal(hist)
	if err != nil {
		log.Printf("WARN: marshal metrics history for %s: %v", projectID, err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, metricsHistoryFile), data, 0644); err != nil {
		log.Printf("WARN: write %s: %v", filepath.Join(dir, metricsHistoryFile), err)
	}
}

func (s *MetricsService) loadHistory(projectID string) []domain.DatabaseMetrics {
	path := filepath.Join(s.storagePath, "projects", projectID, metricsHistoryFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var hist []domain.DatabaseMetrics
	json.Unmarshal(data, &hist)
	return hist
}

// databaseSizeBytes is the customer's data on disk: the project database, and for
// a DocumentDB project also the postgres database its documents live in. A
// DocumentDB project without the postgres figure reports no size rather than
// one that leaves the documents out.
func databaseSizeBytes(labeled map[string]float64, database string, documentDB bool) (int64, bool) {
	app, ok := labeled[`cnpg_pg_database_size_bytes{datname="`+database+`"}`]
	if database == "" || !ok {
		return 0, false
	}
	if !documentDB {
		return int64(app), true
	}
	documents, ok := labeled[`cnpg_pg_database_size_bytes{datname="postgres"}`]
	if !ok {
		return 0, false
	}
	return int64(app) + int64(documents), true
}
