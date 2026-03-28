package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

type MetricsService struct {
	store       storage.InstanceStore
	k8sClient   k8s.KubeClient
	storagePath string
	mu          sync.RWMutex
	history     map[string][]domain.DatabaseMetrics
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

	namespace := inst.Namespace
	projectID := inst.ProjectID

	// Fetch CNPG metrics from port 9187 via pod exec (IPv6)
	cnpgMetrics, err := s.fetchCNPGMetrics(ctx, namespace, projectID)
	if err != nil {
		metrics.MetricsAvailable = false
		reason := err.Error()
		metrics.UnavailableReason = &reason
		return metrics
	}

	if strings.TrimSpace(cnpgMetrics) == "" {
		metrics.MetricsAvailable = false
		reason := "Prometheus exporter returned empty data"
		metrics.UnavailableReason = &reason
		return metrics
	}

	// Parse CNPG metrics
	labeled := parseLabeledMetrics(cnpgMetrics)

	// Connections (sum across all pods for multi-instance)
	tierInstances := tierInstanceCount(inst.Tier)
	totalActive := sumMetric(labeled, "cnpg_backends_total")
	if tierInstances > 1 {
		for i := 2; i <= tierInstances; i++ {
			pod := fmt.Sprintf("%s-postgres-%d", projectID, i)
			replicaRaw, err := s.execMetricsFetch(ctx, namespace, pod)
			if err == nil {
				replicaLabeled := parseLabeledMetrics(replicaRaw)
				totalActive += sumMetric(replicaLabeled, "cnpg_backends_total")
			}
		}
	}
	metrics.ActiveConnections = intPtr(totalActive)

	// Idle connections
	idleCount := sumMetricFiltered(labeled, "cnpg_backends_total", "\"idle\"")
	metrics.IdleConnections = intPtr(idleCount)

	// Max connections
	if v, ok := labeled["cnpg_pg_settings_setting{name=\"max_connections\"}"]; ok {
		metrics.MaxConnections = intPtr(int(v))
	} else {
		metrics.MaxConnections = intPtr(100)
	}

	// Database size
	if v, ok := labeled["cnpg_pg_database_size_bytes{datname=\"app\"}"]; ok {
		gb := int64(v) / (1024 * 1024 * 1024)
		metrics.DatabaseSizeGB = &gb
	}

	// Last backup time
	if v, ok := labeled["cnpg_collector_last_available_backup_timestamp"]; ok && v > 0 {
		t := &domain.FlexTime{Time: time.Unix(int64(v), 0).UTC()}
		metrics.LastBackupTime = t
	}

	// Health status
	if inst.Status == "FAILED" {
		metrics.HealthStatus = "DOWN"
	} else {
		metrics.HealthStatus = "HEALTHY"
	}

	// Resource limits from tier config
	perPodCPU := parseCPU(inst.Tier)
	perPodMemMB := parseMemMB(inst.Tier)
	totalCPULimit := perPodCPU * float64(tierInstances)
	totalMemLimit := int64(perPodMemMB * tierInstances)
	storageLimit := parseStorage(inst.Tier)
	metrics.CPULimitCores = &totalCPULimit
	metrics.MemoryLimitMB = &totalMemLimit
	metrics.StorageLimit = &storageLimit
	metrics.InstanceCount = &tierInstances

	// Pod-level CPU/memory from metrics-server
	podMetrics, err := s.k8sClient.GetPodMetrics(ctx, namespace)
	if err == nil && len(podMetrics) > 0 {
		var totalCPUMillis int64
		var totalMemMB int64
		for _, pm := range podMetrics {
			role := "replica"
			if strings.HasSuffix(pm.Name, "-1") {
				role = "primary"
			}
			metrics.Pods = append(metrics.Pods, domain.PodMetrics{
				Name:          pm.Name,
				Role:          role,
				CPUCores:      float64(pm.CPUMillis) / 1000.0,
				CPULimitCores: perPodCPU,
				MemoryMB:      pm.MemoryMB,
				MemoryLimitMB: int64(perPodMemMB),
			})
			totalCPUMillis += pm.CPUMillis
			totalMemMB += pm.MemoryMB
		}
		// Aggregate CPU/memory as percentages of tier limits
		cpuCores := float64(totalCPUMillis) / 1000.0
		metrics.CPUUsageCores = &cpuCores
		cpuPct := (cpuCores / totalCPULimit) * 100
		metrics.CPUUsagePercent = &cpuPct
		metrics.MemoryUsageMB = &totalMemMB
		memPct := float64(totalMemMB) / float64(totalMemLimit) * 100
		metrics.MemoryUsagePercent = &memPct
	}

	return metrics
}

func (s *MetricsService) fetchCNPGMetrics(ctx context.Context, namespace, projectID string) (string, error) {
	pod := projectID + "-postgres-1"
	return s.execMetricsFetch(ctx, namespace, pod)
}

func (s *MetricsService) execMetricsFetch(ctx context.Context, namespace, pod string) (string, error) {
	return s.k8sClient.ExecInPod(ctx, namespace, pod, "postgres",
		[]string{"python3", "-c", "import urllib.request; print(urllib.request.urlopen('http://[::1]:9187/metrics').read().decode())"})
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

func parseCPU(tier domain.TierType) float64 {
	switch tier {
	case domain.Standard:
		return 2
	case domain.Enterprise:
		return 4
	default:
		return 0.5
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

func parseMemMB(tier domain.TierType) int {
	switch tier {
	case domain.Standard:
		return 4096
	case domain.Enterprise:
		return 16384
	default:
		return 512
	}
}

func (s *MetricsService) saveHistory(projectID string) {
	s.mu.RLock()
	hist := s.history[projectID]
	s.mu.RUnlock()

	dir := filepath.Join(s.storagePath, "projects", projectID)
	os.MkdirAll(dir, 0755)
	data, _ := json.Marshal(hist)
	os.WriteFile(filepath.Join(dir, "metrics-history.json"), data, 0644)
}

func (s *MetricsService) loadHistory(projectID string) []domain.DatabaseMetrics {
	path := filepath.Join(s.storagePath, "projects", projectID, "metrics-history.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var hist []domain.DatabaseMetrics
	json.Unmarshal(data, &hist)
	return hist
}
