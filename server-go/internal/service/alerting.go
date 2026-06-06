package service

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const alertsHistoryFile = "alerts-history.json"

type AlertingService struct {
	storagePath string
	mu          sync.RWMutex
	active      []domain.Alert
	history     []domain.Alert
}

func NewAlertingService(storagePath string) *AlertingService {
	svc := &AlertingService{storagePath: storagePath}
	svc.loadHistory()
	return svc
}

func (s *AlertingService) AddAlert(alert domain.Alert) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = append(s.active, alert)
	s.history = append(s.history, alert)
	if len(s.history) > 1000 {
		s.history = s.history[len(s.history)-1000:]
	}
	s.saveHistory()
}

func (s *AlertingService) GetActiveAlerts() []domain.Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Alert, 0)
	for _, a := range s.active {
		if !a.Resolved {
			result = append(result, a)
		}
	}
	return result
}

func (s *AlertingService) GetActiveAlertsForProject(projectID string) []domain.Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]domain.Alert, 0)
	for _, a := range s.active {
		if a.ProjectID == projectID && !a.Resolved {
			result = append(result, a)
		}
	}
	return result
}

func (s *AlertingService) GetAlertHistory(limit int) []domain.Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.history) {
		limit = len(s.history)
	}
	start := len(s.history) - limit
	if start < 0 {
		start = 0
	}
	return s.history[start:]
}

func (s *AlertingService) CheckMetrics(metrics *domain.DatabaseMetrics) {
	if metrics == nil || !metrics.MetricsAvailable {
		return
	}
	now := &domain.FlexTime{Time: time.Now()}

	if metrics.CPUUsagePercent != nil && *metrics.CPUUsagePercent > 90 {
		s.AddAlert(domain.Alert{
			ID:        fmt.Sprintf("cpu-%s-%d", metrics.ProjectID, time.Now().Unix()),
			ProjectID: metrics.ProjectID,
			Severity:  "WARNING",
			Message:   fmt.Sprintf("CPU usage at %.1f%%", *metrics.CPUUsagePercent),
			Metric:    "cpuUsagePercent",
			Value:     metrics.CPUUsagePercent,
			Threshold: float64Ptr(90),
			Timestamp: now,
		})
	}
	if metrics.MemoryUsagePercent != nil && *metrics.MemoryUsagePercent > 90 {
		s.AddAlert(domain.Alert{
			ID:        fmt.Sprintf("mem-%s-%d", metrics.ProjectID, time.Now().Unix()),
			ProjectID: metrics.ProjectID,
			Severity:  "WARNING",
			Message:   fmt.Sprintf("Memory usage at %.1f%%", *metrics.MemoryUsagePercent),
			Metric:    "memoryUsagePercent",
			Value:     metrics.MemoryUsagePercent,
			Threshold: float64Ptr(90),
			Timestamp: now,
		})
	}
}

func (s *AlertingService) saveHistory() {
	dir := s.storagePath
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("WARN: mkdir %s: %v", dir, err)
		return
	}
	data, err := json.MarshalIndent(s.history, "", "  ")
	if err != nil {
		log.Printf("WARN: marshal alert history: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, alertsHistoryFile), data, 0644); err != nil {
		log.Printf("WARN: write %s: %v", filepath.Join(dir, alertsHistoryFile), err)
	}
}

func (s *AlertingService) loadHistory() {
	data, err := os.ReadFile(filepath.Join(s.storagePath, alertsHistoryFile))
	if err != nil {
		return
	}
	json.Unmarshal(data, &s.history)
}

func float64Ptr(f float64) *float64 { return &f }
