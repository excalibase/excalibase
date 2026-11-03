package service

import (
	"context"
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
	// store, when set, keeps alerts in place of the file (SetStore).
	store AlertStore
}

// AlertStore keeps alerts: the platform DB's alerts table. Lists answer newest first.
type AlertStore interface {
	SaveAlert(ctx context.Context, alert *domain.Alert) error
	GetActiveAlerts(ctx context.Context) ([]domain.Alert, error)
	GetActiveAlertsForProject(ctx context.Context, projectID string) ([]domain.Alert, error)
	GetAlertHistory(ctx context.Context, limit int) ([]domain.Alert, error)
}

// maxAlertHistory bounds a history read, as the file kept the last 1000.
const maxAlertHistory = 1000

// SetStore keeps alerts in the table, shared by every replica.
func (s *AlertingService) SetStore(store AlertStore) {
	s.store = store
}

func NewAlertingService(storagePath string) *AlertingService {
	svc := &AlertingService{storagePath: storagePath}
	svc.loadHistory()
	return svc
}

func (s *AlertingService) AddAlert(alert domain.Alert) {
	if s.store != nil {
		s.save(alert)
		return
	}
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
	if s.store != nil {
		return oldestFirst(s.store.GetActiveAlerts(context.Background()))
	}
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
	if s.store != nil {
		return oldestFirst(s.store.GetActiveAlertsForProject(context.Background(), projectID))
	}
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
	if s.store != nil {
		if limit <= 0 || limit > maxAlertHistory {
			limit = maxAlertHistory
		}
		return oldestFirst(s.store.GetAlertHistory(context.Background(), limit))
	}
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

// alertRule is one usage figure checked against its threshold.
type alertRule struct {
	metric string
	label  string
	value  *float64
}

const usageAlertThreshold = 90

// CheckMetrics raises an alert when a figure goes over the threshold and
// resolves it when the figure falls back, so a figure that stays high stays one alert.
func (s *AlertingService) CheckMetrics(metrics *domain.DatabaseMetrics) {
	if metrics == nil || !metrics.MetricsAvailable {
		return
	}
	active := s.GetActiveAlertsForProject(metrics.ProjectID)
	for _, rule := range []alertRule{
		{metric: "cpuUsagePercent", label: "CPU", value: metrics.CPUUsagePercent},
		{metric: "memoryUsagePercent", label: "Memory", value: metrics.MemoryUsagePercent},
	} {
		if rule.value == nil {
			continue
		}
		open := findAlert(active, rule.metric)
		switch {
		case *rule.value > usageAlertThreshold && open == nil:
			s.AddAlert(domain.Alert{
				ID:        fmt.Sprintf("%s-%s-%d", rule.metric, metrics.ProjectID, time.Now().UnixNano()),
				ProjectID: metrics.ProjectID,
				Severity:  "WARNING",
				Message:   fmt.Sprintf("%s usage at %.1f%%", rule.label, *rule.value),
				Metric:    rule.metric,
				Value:     rule.value,
				Threshold: float64Ptr(usageAlertThreshold),
				Timestamp: &domain.FlexTime{Time: time.Now()},
			})
		case *rule.value <= usageAlertThreshold && open != nil:
			s.resolve(*open)
		}
	}
}

// AlertChange is what one Track call raised or resolved, for notifying.
type AlertChange struct {
	Raised   *domain.Alert
	Resolved *domain.Alert
}

// Track keeps one open alert per project and metric while breached is true
// and resolves it once it is not; newAlert builds the alert to raise.
func (s *AlertingService) Track(projectID, metric string, breached bool, newAlert func() domain.Alert) AlertChange {
	open := findAlert(s.GetActiveAlertsForProject(projectID), metric)
	switch {
	case breached && open == nil:
		alert := newAlert()
		alert.ProjectID, alert.Metric = projectID, metric
		if alert.Timestamp == nil {
			alert.Timestamp = &domain.FlexTime{Time: time.Now()}
		}
		s.AddAlert(alert)
		return AlertChange{Raised: &alert}
	case !breached && open != nil:
		resolved := *open
		s.resolve(resolved)
		resolved.Resolved = true
		return AlertChange{Resolved: &resolved}
	}
	return AlertChange{}
}

func findAlert(alerts []domain.Alert, metric string) *domain.Alert {
	for i := range alerts {
		if alerts[i].Metric == metric {
			return &alerts[i]
		}
	}
	return nil
}

func (s *AlertingService) resolve(alert domain.Alert) {
	alert.Resolved = true
	if s.store != nil {
		s.save(alert)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.active {
		if s.active[i].ID == alert.ID {
			s.active[i].Resolved = true
		}
	}
	for i := range s.history {
		if s.history[i].ID == alert.ID {
			s.history[i].Resolved = true
		}
	}
	s.saveHistory()
}

func (s *AlertingService) save(alert domain.Alert) {
	if err := s.store.SaveAlert(context.Background(), &alert); err != nil {
		log.Printf("alerts: save %s: %v", alert.ID, err)
	}
}

// oldestFirst turns a table read (newest first) into the order the file kept.
func oldestFirst(alerts []domain.Alert, err error) []domain.Alert {
	if err != nil {
		log.Printf("alerts: read: %v", err)
		return []domain.Alert{}
	}
	for i, j := 0, len(alerts)-1; i < j; i, j = i+1, j-1 {
		alerts[i], alerts[j] = alerts[j], alerts[i]
	}
	return alerts
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
