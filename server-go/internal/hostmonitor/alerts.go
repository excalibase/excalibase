package hostmonitor

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// hostRule is one host figure, averaged over the window, against its threshold.
type hostRule struct {
	metric    string
	label     string
	unit      string
	threshold *float64
	value     func(hostmetrics.Averages) *float64
}

func hostRules(settings domain.MonitoringAlertSettings) []hostRule {
	return []hostRule{
		{"host.cpu", "Host CPU", "%", settings.CPUPercent, func(a hostmetrics.Averages) *float64 { return a.CPUPercent }},
		{"host.memory", "Host memory", "%", settings.MemoryPercent, func(a hostmetrics.Averages) *float64 { return a.MemoryPercent }},
		{"host.disk", "Host disk", "%", settings.DiskPercent, func(a hostmetrics.Averages) *float64 { return a.DiskPercent }},
		{"host.temperature", "Host temperature", " °C", settings.TemperatureCelsius, func(a hostmetrics.Averages) *float64 { return a.TemperatureCelsius }},
		{"host.load", "Host load (5 min)", "", settings.Load5, func(a hostmetrics.Averages) *float64 { return a.Load5 }},
		{"host.network", "Host network", " Mbit/s", settings.NetworkMbps, func(a hostmetrics.Averages) *float64 {
			if a.NetworkBytesPerSec == nil {
				return nil
			}
			mbps := *a.NetworkBytesPerSec * 8 / 1e6
			return &mbps
		}},
	}
}

// settings are the saved rules, or the defaults before an admin saves any.
func (m *Monitor) settings(ctx context.Context) (domain.MonitoringAlertSettings, bool) {
	saved, found, err := m.deps.Store.GetMonitoringAlertSettings(ctx)
	if err != nil {
		log.Printf("host monitor: alert settings: %v", err)
		return domain.MonitoringAlertSettings{}, false
	}
	if !found {
		return domain.DefaultMonitoringAlertSettings(), true
	}
	return saved, true
}

func (m *Monitor) evaluate(ctx context.Context, now time.Time, instances []*domain.DatabaseInstance, samples []domain.ContainerSample) {
	if m.deps.Alerts == nil {
		return
	}
	settings, ok := m.settings(ctx)
	if !ok {
		return
	}
	m.evaluateHost(ctx, now, settings)
	m.evaluateContainers(ctx, now, settings, instances, samples)
}

func (m *Monitor) evaluateHost(ctx context.Context, now time.Time, settings domain.MonitoringAlertSettings) {
	window := max(settings.WindowMinutes, 1)
	averages, err := m.deps.Store.HostAverages(ctx, now.Add(-time.Duration(window)*time.Minute))
	if err != nil {
		log.Printf("host monitor: alert window: %v", err)
		return
	}
	// Half the window's minutes must be there, so one sample alone never alerts.
	enough := averages.Samples >= (window+1)/2
	for _, rule := range hostRules(settings) {
		value := rule.value(averages)
		if settings.Enabled && rule.threshold != nil && value != nil && !enough {
			continue
		}
		breached := settings.Enabled && rule.threshold != nil && value != nil && *value > *rule.threshold
		change := m.deps.Alerts.Track("", rule.metric, breached, func() domain.Alert {
			return domain.Alert{
				ID: fmt.Sprintf("%s-%d", rule.metric, now.UnixNano()), Severity: "WARNING",
				Message: fmt.Sprintf("%s averaged %.1f%s over %d min (threshold %.1f%s)", rule.label, *value, rule.unit, window, *rule.threshold, rule.unit),
				Value:   value, Threshold: rule.threshold, Timestamp: &domain.FlexTime{Time: now},
			}
		})
		m.notify(ctx, settings, change)
	}
}

// expectedRunning are the containers whose absence is an outage: the
// platform's own and the database of every active project.
func expectedRunning(instances []*domain.DatabaseInstance, samples []domain.ContainerSample) map[string]bool {
	expected := map[string]bool{}
	active := map[string]bool{}
	for _, inst := range instances {
		if inst.DeploymentMode == domain.ModeDocker && inst.Status == string(domain.StatusActive) && !inst.NoDatabase {
			active[inst.ProjectID] = true
		}
	}
	for _, sample := range samples {
		if sample.Component == domain.ContainerPlatform || (sample.Component == domain.ContainerDatabase && active[sample.ProjectID]) {
			expected[sample.Name] = true
		}
	}
	return expected
}

func (m *Monitor) evaluateContainers(ctx context.Context, now time.Time, settings domain.MonitoringAlertSettings, instances []*domain.DatabaseInstance, samples []domain.ContainerSample) {
	expected := expectedRunning(instances, samples)
	down := time.Duration(max(settings.DownMinutes, 0)) * time.Minute
	stillBad := map[string]time.Time{}
	breachedNow := map[string]bool{}
	for _, sample := range samples {
		problem := containerProblem(sample, expected[sample.Name])
		if problem == "" {
			continue
		}
		since, seen := m.badSince[sample.Name]
		if !seen {
			since = now
		}
		stillBad[sample.Name] = since
		if !settings.Enabled || now.Sub(since) < down {
			continue
		}
		breachedNow[containerAlertMetric(sample.Name)] = true
		change := m.deps.Alerts.Track(sample.ProjectID, containerAlertMetric(sample.Name), true, func() domain.Alert {
			return domain.Alert{
				ID: fmt.Sprintf("container-%s-%d", sample.Name, now.UnixNano()), Severity: "CRITICAL",
				Message: fmt.Sprintf("Container %s is %s", sample.Name, problem), Timestamp: &domain.FlexTime{Time: now},
			}
		})
		m.notify(ctx, settings, change)
	}
	m.badSince = stillBad
	// Recovered, removed or no longer expected: resolve what is still open.
	for _, open := range m.deps.Alerts.GetActiveAlerts() {
		if strings.HasPrefix(open.Metric, "container.down:") && !breachedNow[open.Metric] {
			m.notify(ctx, settings, m.deps.Alerts.Track(open.ProjectID, open.Metric, false, nil))
		}
	}
}

// containerProblem says what is wrong with a container, or "" when nothing is.
func containerProblem(sample domain.ContainerSample, expected bool) string {
	switch {
	case sample.Health == "unhealthy":
		return "unhealthy"
	case !expected:
		return ""
	case sample.State == StateMissing:
		return "missing"
	case sample.State != "running":
		return "not running (" + sample.State + ")"
	}
	return ""
}

func (m *Monitor) notify(ctx context.Context, settings domain.MonitoringAlertSettings, change service.AlertChange) {
	if m.deps.Notifier == nil || (change.Raised == nil && change.Resolved == nil) {
		return
	}
	m.deps.Notifier.Notify(ctx, settings, change)
}
