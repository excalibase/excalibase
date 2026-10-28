package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/hostmetrics"
	"github.com/excalibase/provisioning-poc/internal/hostmonitor"
)

type fakeMonitoringReads struct {
	since     time.Time
	bucket    time.Duration
	name      string
	projectID string
	latest    *hostmetrics.Sample
	err       error
}

var monitoredAt = time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)

func (f *fakeMonitoringReads) LatestHostSample(context.Context) (*hostmetrics.Sample, error) {
	return f.latest, f.err
}

func (f *fakeMonitoringReads) HostHistory(_ context.Context, since time.Time, bucket time.Duration) ([]hostmetrics.Sample, error) {
	f.since, f.bucket = since, bucket
	return []hostmetrics.Sample{{Timestamp: monitoredAt}}, f.err
}

func (f *fakeMonitoringReads) LatestContainerSamples(context.Context) ([]domain.ContainerSample, *time.Time, error) {
	at := monitoredAt
	return []domain.ContainerSample{
		{Name: "excalibase-postgres", Component: domain.ContainerPlatform, State: "running", Timestamp: at},
		{Name: "excalibase-p1-postgres", ProjectID: "p1", Component: domain.ContainerDatabase, State: "running", Timestamp: at},
		{Name: "excalibase-p2-postgres", ProjectID: "p2", Component: domain.ContainerDatabase, State: "running", Timestamp: at},
	}, &at, f.err
}

func (f *fakeMonitoringReads) ContainerHistory(_ context.Context, name string, since time.Time, bucket time.Duration) ([]domain.ContainerSample, error) {
	f.name, f.since, f.bucket = name, since, bucket
	return []domain.ContainerSample{{Name: name, Timestamp: monitoredAt}}, f.err
}

func (f *fakeMonitoringReads) ProjectContainerHistory(_ context.Context, projectID string, since time.Time, bucket time.Duration) ([]domain.ContainerSample, error) {
	f.projectID, f.since, f.bucket = projectID, since, bucket
	return []domain.ContainerSample{
		{Name: "excalibase-p1-postgres", ProjectID: projectID, Timestamp: monitoredAt.Add(-time.Minute)},
		{Name: "excalibase-p1-postgres", ProjectID: projectID, Timestamp: monitoredAt},
		{Name: "excalibase-p1-watcher", ProjectID: projectID, Component: domain.ContainerWatcher, State: "exited", Timestamp: monitoredAt},
	}, f.err
}

type fakeAlertSettings struct {
	update  *hostmonitor.SettingsUpdate
	err     error
	results []hostmonitor.ChannelResult
}

func (f *fakeAlertSettings) Get(context.Context) (hostmonitor.SettingsView, error) {
	return hostmonitor.SettingsView{MonitoringAlertSettings: domain.DefaultMonitoringAlertSettings()}, f.err
}

func (f *fakeAlertSettings) Update(_ context.Context, update hostmonitor.SettingsUpdate) (hostmonitor.SettingsView, error) {
	f.update = &update
	if f.err != nil {
		return hostmonitor.SettingsView{}, f.err
	}
	display := "https://hooks.example.com/..."
	return hostmonitor.SettingsView{MonitoringAlertSettings: domain.MonitoringAlertSettings{Enabled: update.Enabled, Emails: []string{}}, WebhookURL: &display, WebhookSet: true}, nil
}

func (f *fakeAlertSettings) Test(context.Context) ([]hostmonitor.ChannelResult, error) {
	return f.results, f.err
}

func monitoringRouter(reads *fakeMonitoringReads, settings *fakeAlertSettings, audit *recordedAudit) chi.Router {
	h := NewHostMonitoringHandler(reads, settings, audit)
	h.now = func() time.Time { return monitoredAt }
	r := chi.NewRouter()
	r.Route("/api/admin/monitoring", func(r chi.Router) {
		h.ReadRoutes(r)
		h.WriteRoutes(r)
	})
	r.Get("/api/projects/{projectId}/containers/metrics", h.ProjectContainers)
	return r
}

func serveMonitoring(r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
	return w
}

func TestHostReadAnswersTheRangeInBuckets(t *testing.T) {
	cases := map[string]time.Duration{"": time.Minute, "1h": time.Minute, "6h": time.Minute, "24h": 5 * time.Minute, "7d": 30 * time.Minute, "30d": 2 * time.Hour}
	for rangeName, bucket := range cases {
		reads := &fakeMonitoringReads{latest: &hostmetrics.Sample{Timestamp: monitoredAt}}
		w := serveMonitoring(monitoringRouter(reads, &fakeAlertSettings{}, nil), http.MethodGet, "/api/admin/monitoring/host?range="+rangeName, "")
		if w.Code != http.StatusOK || reads.bucket != bucket {
			t.Errorf("range %q: %d, bucket %v want %v", rangeName, w.Code, reads.bucket, bucket)
			continue
		}
		var body struct {
			Range         string
			BucketSeconds int
			Current       *hostmetrics.Sample
			Points        []hostmetrics.Sample
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Current == nil || len(body.Points) != 1 || body.BucketSeconds != int(bucket.Seconds()) {
			t.Errorf("range %q body = %s", rangeName, w.Body.String())
		}
	}
	if w := serveMonitoring(monitoringRouter(&fakeMonitoringReads{}, &fakeAlertSettings{}, nil), http.MethodGet, "/api/admin/monitoring/host?range=1y", ""); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown range = %d", w.Code)
	}
}

func TestAReadFailureAnswersWithoutTheCause(t *testing.T) {
	reads := &fakeMonitoringReads{err: errors.New("pq: relation host_metrics at 10.0.0.3")}
	for _, path := range []string{"/api/admin/monitoring/host", "/api/admin/monitoring/containers", "/api/admin/monitoring/containers/excalibase-nats", "/api/projects/p1/containers/metrics"} {
		w := serveMonitoring(monitoringRouter(reads, &fakeAlertSettings{}, nil), http.MethodGet, path, "")
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "10.0.0.3") {
			t.Errorf("%s = %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestContainersAnswerTheNewestMinute(t *testing.T) {
	w := serveMonitoring(monitoringRouter(&fakeMonitoringReads{}, &fakeAlertSettings{}, nil), http.MethodGet, "/api/admin/monitoring/containers", "")
	var body struct {
		Timestamp  *time.Time
		Containers []domain.ContainerSample
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Containers) != 3 || body.Timestamp == nil {
		t.Errorf("containers = %s", w.Body.String())
	}
}

func TestContainerHistoryTakesOnlyAContainerName(t *testing.T) {
	reads := &fakeMonitoringReads{}
	w := serveMonitoring(monitoringRouter(reads, &fakeAlertSettings{}, nil), http.MethodGet, "/api/admin/monitoring/containers/excalibase-p1-postgres?range=24h", "")
	if w.Code != http.StatusOK || reads.name != "excalibase-p1-postgres" || reads.bucket != 5*time.Minute || !reads.since.Equal(monitoredAt.Add(-24*time.Hour)) {
		t.Errorf("history = %d, asked %q %v %v", w.Code, reads.name, reads.bucket, reads.since)
	}
	for _, name := range []string{"-x", "a%20b", strings.Repeat("a", 200)} {
		if w := serveMonitoring(monitoringRouter(reads, &fakeAlertSettings{}, nil), http.MethodGet, "/api/admin/monitoring/containers/"+name, ""); w.Code != http.StatusBadRequest {
			t.Errorf("name %q = %d", name, w.Code)
		}
	}
}

func TestAProjectSeesOnlyItsOwnContainers(t *testing.T) {
	reads := &fakeMonitoringReads{}
	w := serveMonitoring(monitoringRouter(reads, &fakeAlertSettings{}, nil), http.MethodGet, "/api/projects/p1/containers/metrics?range=6h", "")
	if w.Code != http.StatusOK || reads.projectID != "p1" {
		t.Fatalf("project containers = %d asked %q", w.Code, reads.projectID)
	}
	if strings.Contains(w.Body.String(), "p2") || strings.Contains(w.Body.String(), `"excalibase-postgres"`) {
		t.Errorf("another tenant's or the platform's container leaked: %s", w.Body.String())
	}
	var body struct {
		Containers []struct {
			Name    string
			State   string
			Current *domain.ContainerSample
			Points  []domain.ContainerSample
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Containers) != 2 {
		t.Fatalf("body = %s", w.Body.String())
	}
	db := body.Containers[0]
	if db.Name != "excalibase-p1-postgres" || db.Current == nil || db.State != "running" || len(db.Points) != 2 {
		t.Errorf("database = %+v", db)
	}
	if watcher := body.Containers[1]; watcher.Current != nil || watcher.State != "exited" || len(watcher.Points) != 1 {
		t.Errorf("a container missing from the newest minute = %+v", watcher)
	}
}

func TestAlertSettingsSaveAuditsWithoutTheWebhook(t *testing.T) {
	settings, audit := &fakeAlertSettings{}, &recordedAudit{}
	body := `{"enabled":true,"cpuPercent":80,"windowMinutes":5,"downMinutes":2,"emails":["ops@example.com"],"webhookUrl":"https://hooks.example.com/T/secret"}`
	w := serveMonitoring(monitoringRouter(&fakeMonitoringReads{}, settings, audit), http.MethodPut, "/api/admin/monitoring/alert-settings", body)
	if w.Code != http.StatusOK || settings.update == nil || *settings.update.CPUPercent != 80 || *settings.update.WebhookURL != "https://hooks.example.com/T/secret" {
		t.Fatalf("save = %d %s, update %+v", w.Code, w.Body.String(), settings.update)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("the answer carries the webhook's token: %s", w.Body.String())
	}
	if len(audit.entries) != 1 || audit.entries[0].Resource != "monitoring_alert_settings" || strings.Contains(audit.entries[0].Details, "secret") {
		t.Errorf("audit = %+v", audit.entries)
	}
}

func TestAlertSettingsRefusalsAreTheirOwnStatus(t *testing.T) {
	cases := map[error]int{
		hostmonitor.ErrInvalidSettings:                   http.StatusBadRequest,
		hostmonitor.ErrVaultSealed:                       http.StatusServiceUnavailable,
		errors.New("pq: connection to 10.0.0.3 refused"): http.StatusInternalServerError,
	}
	for cause, status := range cases {
		w := serveMonitoring(monitoringRouter(&fakeMonitoringReads{}, &fakeAlertSettings{err: cause}, &recordedAudit{}), http.MethodPut, "/api/admin/monitoring/alert-settings", `{"windowMinutes":5}`)
		if w.Code != status || strings.Contains(w.Body.String(), "10.0.0.3") {
			t.Errorf("%v = %d %s", cause, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{`, `{"unknown":1}`, `[]`} {
		if w := serveMonitoring(monitoringRouter(&fakeMonitoringReads{}, &fakeAlertSettings{}, nil), http.MethodPut, "/api/admin/monitoring/alert-settings", body); w.Code != http.StatusBadRequest {
			t.Errorf("body %s = %d", body, w.Code)
		}
	}
}

func TestAlertSettingsReadAndTest(t *testing.T) {
	settings := &fakeAlertSettings{results: []hostmonitor.ChannelResult{{Channel: "email", OK: true}}}
	w := serveMonitoring(monitoringRouter(&fakeMonitoringReads{}, settings, nil), http.MethodGet, "/api/admin/monitoring/alert-settings", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"cpuPercent":90`) || !strings.Contains(w.Body.String(), `"webhookSet":false`) {
		t.Errorf("read = %d %s", w.Code, w.Body.String())
	}
	w = serveMonitoring(monitoringRouter(&fakeMonitoringReads{}, settings, nil), http.MethodPost, "/api/admin/monitoring/alert-settings/test", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"results":[{"channel":"email","ok":true}]`) {
		t.Errorf("test = %d %s", w.Code, w.Body.String())
	}
}
