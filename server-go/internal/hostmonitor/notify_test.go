package hostmonitor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type sentMail struct {
	mu       sync.Mutex
	messages []email.Message
	err      error
}

func (s *sentMail) Send(_ context.Context, msg email.Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	s.messages = append(s.messages, msg)
	return "id", nil
}

type memoryVault struct {
	mu      sync.Mutex
	entries map[string]map[string]string
	sealed  bool
	err     error
}

func (v *memoryVault) Get(path string) (map[string]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return nil, v.err
	}
	entry, ok := v.entries[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return entry, nil
}

func (v *memoryVault) Put(path string, data map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return v.err
	}
	if v.entries == nil {
		v.entries = map[string]map[string]string{}
	}
	v.entries[path] = data
	return nil
}

func (v *memoryVault) Delete(path string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.entries, path)
	return v.err
}

func (v *memoryVault) Sealed() bool { return v.sealed }

type hook struct {
	mu     sync.Mutex
	bodies []map[string]any
	status int
}

func webhookServer(t *testing.T, status int) (*hook, string) {
	t.Helper()
	received := &hook{status: status}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		received.mu.Lock()
		received.bodies = append(received.bodies, body)
		received.mu.Unlock()
		if r.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		w.WriteHeader(received.status)
	}))
	t.Cleanup(server.Close)
	return received, server.URL + "/hooks/T000/secret-token"
}

func raisedCPU() service.AlertChange {
	value, threshold := 95.0, 90.0
	return service.AlertChange{Raised: &domain.Alert{ID: "host.cpu-1", Severity: "WARNING", Metric: "host.cpu",
		Message: "Host CPU averaged 95.0% over 5 min (threshold 90.0%)", Value: &value, Threshold: &threshold}}
}

func TestNotifySendsEveryRecipientItsOwnMailAndTheWebhookAPost(t *testing.T) {
	mail := &sentMail{}
	received, url := webhookServer(t, http.StatusOK)
	vault := &memoryVault{entries: map[string]map[string]string{WebhookVaultPath: {"url": url}}}
	notifier := NewNotifier(NotifierDeps{Mail: mail, From: "alerts@example.com", FromName: "Excalibase", Vault: vault,
		Client: http.DefaultClient, StudioURL: "https://studio.example.com"})
	settings := domain.MonitoringAlertSettings{Emails: []string{"a@example.com", "b@example.com"}, WebhookDisplay: "http://127.0.0.1/..."}
	notifier.Notify(context.Background(), settings, raisedCPU())

	if len(mail.messages) != 2 || len(mail.messages[0].To) != 1 || mail.messages[0].To[0] != "a@example.com" {
		t.Fatalf("mail = %+v", mail.messages)
	}
	first := mail.messages[0]
	if !strings.Contains(first.Subject, "Host CPU averaged 95.0%") || !strings.Contains(first.TextBody, "https://studio.example.com/admin/host") || first.From != "alerts@example.com" {
		t.Errorf("message = %+v", first)
	}
	if len(received.bodies) != 1 {
		t.Fatalf("webhook got %d posts", len(received.bodies))
	}
	body := received.bodies[0]
	if body["event"] != "alert.raised" || !strings.Contains(body["text"].(string), "Host CPU averaged 95.0%") {
		t.Errorf("webhook body = %v", body)
	}
	alert := body["alert"].(map[string]any)
	if alert["metric"] != "host.cpu" || alert["severity"] != "WARNING" {
		t.Errorf("webhook alert = %v", alert)
	}
}

func TestAResolveSaysSoAndAProjectAlertLinksTheProject(t *testing.T) {
	mail := &sentMail{}
	notifier := NewNotifier(NotifierDeps{Mail: mail, From: "alerts@example.com", StudioURL: "https://studio.example.com"})
	resolved := domain.Alert{ID: "c-1", ProjectID: "p1", Severity: "CRITICAL", Metric: "container.down:excalibase-shop-postgres",
		Message: "Container excalibase-shop-postgres is not running (exited)", Resolved: true}
	notifier.Notify(context.Background(), domain.MonitoringAlertSettings{Emails: []string{"a@example.com"}}, service.AlertChange{Resolved: &resolved})
	if len(mail.messages) != 1 {
		t.Fatalf("mail = %+v", mail.messages)
	}
	if !strings.HasPrefix(mail.messages[0].Subject, "[Excalibase] Resolved:") || !strings.Contains(mail.messages[0].TextBody, "/project/p1/monitoring/metrics") {
		t.Errorf("resolve mail = %+v", mail.messages[0])
	}
}

func TestNoChannelNoSend(t *testing.T) {
	vault := &memoryVault{err: errors.New("must not be read")}
	notifier := NewNotifier(NotifierDeps{Vault: vault})
	notifier.Notify(context.Background(), domain.MonitoringAlertSettings{}, raisedCPU())
	if results := notifier.Test(context.Background(), domain.MonitoringAlertSettings{}); len(results) != 0 {
		t.Errorf("no channel configured, nothing tested: %+v", results)
	}
}

func TestTestReportsEachChannelWithoutInternalDetail(t *testing.T) {
	mail := &sentMail{err: errors.New("smtp 10.0.0.5:25: connection refused")}
	_, url := webhookServer(t, http.StatusNotFound)
	vault := &memoryVault{entries: map[string]map[string]string{WebhookVaultPath: {"url": url}}}
	notifier := NewNotifier(NotifierDeps{Mail: mail, Vault: vault, Client: http.DefaultClient})
	results := notifier.Test(context.Background(), domain.MonitoringAlertSettings{Emails: []string{"a@example.com"}, WebhookDisplay: "x"})
	if len(results) != 2 || results[0].Channel != "email" || results[0].OK || results[1].Channel != "webhook" || results[1].OK {
		t.Fatalf("results = %+v", results)
	}
	if strings.Contains(results[0].Error, "10.0.0.5") || results[1].Error != "the webhook answered 404" {
		t.Errorf("errors = %q / %q", results[0].Error, results[1].Error)
	}
	mail.err = nil
	_, okURL := webhookServer(t, http.StatusNoContent)
	vault.entries[WebhookVaultPath] = map[string]string{"url": okURL}
	for _, result := range notifier.Test(context.Background(), domain.MonitoringAlertSettings{Emails: []string{"a@example.com"}, WebhookDisplay: "x"}) {
		if !result.OK {
			t.Errorf("%s failed: %s", result.Channel, result.Error)
		}
	}
}

func TestTheGuardedClientRefusesAnInternalWebhook(t *testing.T) {
	_, url := webhookServer(t, http.StatusOK)
	vault := &memoryVault{entries: map[string]map[string]string{WebhookVaultPath: {"url": url}}}
	notifier := NewNotifier(NotifierDeps{Vault: vault, Client: GuardedClient(nil)})
	results := notifier.Test(context.Background(), domain.MonitoringAlertSettings{WebhookDisplay: "x"})
	if len(results) != 1 || results[0].OK || results[0].Error != "the webhook could not be reached" {
		t.Errorf("a loopback webhook = %+v", results)
	}
}

func TestASubjectStaysOneLine(t *testing.T) {
	if got := oneLine("[Excalibase] Alert: Container x\r\nBcc: evil@example.com"); strings.ContainsAny(got, "\r\n") {
		t.Errorf("subject = %q", got)
	}
}

// The install's own off-host ranges (APP_EGRESS_EXTRA_DENY_CIDRS) are refused
// to the webhook as to apps: the host's public address is not a destination.
func TestTheWebhookClientRefusesTheInstallsOwnRanges(t *testing.T) {
	client := GuardedClient([]netip.Prefix{netip.MustParsePrefix("1.1.1.0/24")})
	_, err := client.Get("https://1.1.1.1/hook")
	if err == nil || !strings.Contains(err.Error(), "not a publicly routable address") {
		t.Errorf("err = %v, want the address refused", err)
	}
}
