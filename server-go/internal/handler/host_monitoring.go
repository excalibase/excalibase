package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/excalibase/provisioning-poc/internal/hostmonitor"
)

// MonitoringReads are the stored host and container minutes (EXC-670).
type MonitoringReads interface {
	LatestHostSample(ctx context.Context) (*hostmetrics.Sample, error)
	HostHistory(ctx context.Context, since time.Time, bucket time.Duration) ([]hostmetrics.Sample, error)
	LatestContainerSamples(ctx context.Context) ([]domain.ContainerSample, *time.Time, error)
	ContainerHistory(ctx context.Context, name string, since time.Time, bucket time.Duration) ([]domain.ContainerSample, error)
	ProjectContainerHistory(ctx context.Context, projectID string, since time.Time, bucket time.Duration) ([]domain.ContainerSample, error)
}

// AlertSettingsManager reads, changes and tests the host's alert rules.
type AlertSettingsManager interface {
	Get(ctx context.Context) (hostmonitor.SettingsView, error)
	Update(ctx context.Context, update hostmonitor.SettingsUpdate) (hostmonitor.SettingsView, error)
	Test(ctx context.Context) ([]hostmonitor.ChannelResult, error)
}

// monitoringRange is how far back a range reads and its bucket: at most 360 points.
type monitoringRange struct {
	span, bucket time.Duration
}

var monitoringRanges = map[string]monitoringRange{
	"1h":  {time.Hour, time.Minute},
	"6h":  {6 * time.Hour, time.Minute},
	"24h": {24 * time.Hour, 5 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 30 * time.Minute},
	"30d": {30 * 24 * time.Hour, 2 * time.Hour},
}

var monitoredName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// HostMonitoringHandler serves a single host's monitoring to Studio.
type HostMonitoringHandler struct {
	reads    MonitoringReads
	settings AlertSettingsManager
	audit    auditWriter
	now      func() time.Time
}

// NewHostMonitoringHandler takes an optional audit log; nil records nothing.
func NewHostMonitoringHandler(reads MonitoringReads, settings AlertSettingsManager, audit auditWriter) *HostMonitoringHandler {
	return &HostMonitoringHandler{reads: reads, settings: settings, audit: audit, now: time.Now}
}

// ReadRoutes are the platform readers' routes.
func (h *HostMonitoringHandler) ReadRoutes(r chi.Router) {
	r.Get("/host", h.Host)
	r.Get("/containers", h.Containers)
	r.Get("/containers/{name}", h.ContainerHistory)
	r.Get("/alert-settings", h.GetAlertSettings)
}

// WriteRoutes change the alert rules; the mount decides who may.
func (h *HostMonitoringHandler) WriteRoutes(r chi.Router) {
	r.Put("/alert-settings", h.PutAlertSettings)
	r.Post("/alert-settings/test", h.TestAlertSettings)
}

func (h *HostMonitoringHandler) rangeOf(w http.ResponseWriter, r *http.Request) (string, monitoringRange, bool) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "1h"
	}
	chosen, ok := monitoringRanges[name]
	if !ok {
		httpError(w, "range must be 1h, 6h, 24h, 7d or 30d", http.StatusBadRequest)
	}
	return name, chosen, ok
}

func (h *HostMonitoringHandler) failed(w http.ResponseWriter, what string, err error) {
	log.Printf("host monitoring: %s: %v", what, err)
	httpError(w, "monitoring data could not be read", http.StatusInternalServerError)
}

func (h *HostMonitoringHandler) Host(w http.ResponseWriter, r *http.Request) {
	name, chosen, ok := h.rangeOf(w, r)
	if !ok {
		return
	}
	current, err := h.reads.LatestHostSample(r.Context())
	if err != nil {
		h.failed(w, "latest host sample", err)
		return
	}
	points, err := h.reads.HostHistory(r.Context(), h.now().Add(-chosen.span), chosen.bucket)
	if err != nil {
		h.failed(w, "host history", err)
		return
	}
	writeJSON(w, map[string]any{"range": name, "bucketSeconds": int(chosen.bucket.Seconds()), "current": current, "points": points})
}

func (h *HostMonitoringHandler) Containers(w http.ResponseWriter, r *http.Request) {
	containers, at, err := h.reads.LatestContainerSamples(r.Context())
	if err != nil {
		h.failed(w, "latest containers", err)
		return
	}
	writeJSON(w, map[string]any{"timestamp": at, "containers": containers})
}

func (h *HostMonitoringHandler) ContainerHistory(w http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(chi.URLParam(r, "name"))
	if err != nil || !monitoredName.MatchString(name) {
		httpError(w, "not a container name", http.StatusBadRequest)
		return
	}
	rangeName, chosen, ok := h.rangeOf(w, r)
	if !ok {
		return
	}
	points, err := h.reads.ContainerHistory(r.Context(), name, h.now().Add(-chosen.span), chosen.bucket)
	if err != nil {
		h.failed(w, "container history", err)
		return
	}
	writeJSON(w, map[string]any{"name": name, "range": rangeName, "bucketSeconds": int(chosen.bucket.Seconds()), "points": points})
}

// projectContainer is one of a project's containers with its newest minute and history.
type projectContainer struct {
	Name      string                   `json:"name"`
	Component string                   `json:"component"`
	State     string                   `json:"state"`
	Health    string                   `json:"health"`
	Current   *domain.ContainerSample  `json:"current"`
	Points    []domain.ContainerSample `json:"points"`
}

// ProjectContainers answers the project's own containers, never another's.
func (h *HostMonitoringHandler) ProjectContainers(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	rangeName, chosen, ok := h.rangeOf(w, r)
	if !ok {
		return
	}
	latest, _, err := h.reads.LatestContainerSamples(r.Context())
	if err != nil {
		h.failed(w, "latest containers", err)
		return
	}
	history, err := h.reads.ProjectContainerHistory(r.Context(), projectID, h.now().Add(-chosen.span), chosen.bucket)
	if err != nil {
		h.failed(w, "project containers", err)
		return
	}
	writeJSON(w, map[string]any{"range": rangeName, "bucketSeconds": int(chosen.bucket.Seconds()),
		"containers": groupProjectContainers(projectID, latest, history)})
}

func groupProjectContainers(projectID string, latest, history []domain.ContainerSample) []projectContainer {
	var order []string
	byName := map[string]*projectContainer{}
	entry := func(sample domain.ContainerSample) *projectContainer {
		if found, ok := byName[sample.Name]; ok {
			return found
		}
		created := &projectContainer{Name: sample.Name, Component: sample.Component, State: sample.State, Health: sample.Health, Points: []domain.ContainerSample{}}
		byName[sample.Name] = created
		order = append(order, sample.Name)
		return created
	}
	for _, sample := range latest {
		if sample.ProjectID != projectID {
			continue
		}
		current := sample
		found := entry(sample)
		found.Current, found.Component, found.State, found.Health = &current, sample.Component, sample.State, sample.Health
	}
	for _, sample := range history {
		if sample.ProjectID != projectID {
			continue
		}
		found := entry(sample)
		found.Points = append(found.Points, sample)
		if found.Current == nil {
			found.State, found.Health = sample.State, sample.Health
		}
	}
	containers := make([]projectContainer, 0, len(order))
	for _, name := range order {
		containers = append(containers, *byName[name])
	}
	return containers
}

func (h *HostMonitoringHandler) GetAlertSettings(w http.ResponseWriter, r *http.Request) {
	view, err := h.settings.Get(r.Context())
	if err != nil {
		h.failed(w, "alert settings", err)
		return
	}
	writeJSON(w, view)
}

func (h *HostMonitoringHandler) PutAlertSettings(w http.ResponseWriter, r *http.Request) {
	var update hostmonitor.SettingsUpdate
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&update); err != nil {
		httpError(w, "the alert settings are not valid JSON", http.StatusBadRequest)
		return
	}
	view, err := h.settings.Update(r.Context(), update)
	switch {
	case errors.Is(err, hostmonitor.ErrInvalidSettings):
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, hostmonitor.ErrVaultSealed):
		httpError(w, "the vault is sealed; unseal it to change the webhook", http.StatusServiceUnavailable)
		return
	case err != nil:
		log.Printf("host monitoring: save alert settings: %v", err)
		httpError(w, "the alert settings could not be saved", http.StatusInternalServerError)
		return
	}
	h.record(r, view)
	writeJSON(w, view)
}

func (h *HostMonitoringHandler) TestAlertSettings(w http.ResponseWriter, r *http.Request) {
	results, err := h.settings.Test(r.Context())
	if err != nil {
		h.failed(w, "test notice", err)
		return
	}
	if results == nil {
		results = []hostmonitor.ChannelResult{}
	}
	writeJSON(w, map[string]any{"results": results})
}

// record audits a change of rules by the webhook's host, never its URL.
func (h *HostMonitoringHandler) record(r *http.Request, view hostmonitor.SettingsView) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{"enabled": view.Enabled, "cpuPercent": view.CPUPercent, "memoryPercent": view.MemoryPercent,
		"diskPercent": view.DiskPercent, "temperatureCelsius": view.TemperatureCelsius, "load5": view.Load5, "networkMbps": view.NetworkMbps,
		"windowMinutes": view.WindowMinutes, "downMinutes": view.DownMinutes, "recipients": len(view.Emails), "webhook": view.WebhookURL})
	now := h.now()
	entry := &domain.AuditEntry{UserID: actorID(r), Action: "update", Resource: "monitoring_alert_settings", ResourceID: "host",
		Details: string(details), IPAddress: clientIP(r), Timestamp: &now}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit alert settings: %v", err)
	}
}
