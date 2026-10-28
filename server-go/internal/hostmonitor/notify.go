package hostmonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
	"github.com/excalibase/provisioning-poc/internal/publicaddr"
	"github.com/excalibase/provisioning-poc/internal/service"
)

// WebhookVaultPath holds the alert webhook's URL; it often carries a token.
const WebhookVaultPath = "monitoring/alert-webhook"

const notifyTimeout = 15 * time.Second

// Vault is the slice of the vault the alert webhook needs.
type Vault interface {
	Get(path string) (map[string]string, error)
	Put(path string, data map[string]string) error
	Delete(path string) error
	Sealed() bool
}

// NotifierDeps builds a Notifier; a nil Mail sends no email.
type NotifierDeps struct {
	Mail      email.Sender
	From      string
	FromName  string
	Vault     Vault
	Client    *http.Client
	StudioURL string
}

// Notifier sends alerts by email and to the webhook.
type Notifier struct {
	deps NotifierDeps
}

func NewNotifier(deps NotifierDeps) *Notifier {
	return &Notifier{deps: deps}
}

// GuardedClient reaches only public addresses, checked on every connect
// (the EXC-658 dialer), and never follows a redirect.
func GuardedClient() *http.Client {
	return logdrain.NewHTTPClient(publicaddr.NewDialer(nil, nil))
}

// ChannelResult is one channel's answer to a test notice.
type ChannelResult struct {
	Channel string `json:"channel"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
}

// notice is one message, in the words every channel uses.
type notice struct {
	event   string
	subject string
	text    string
	alert   *domain.Alert
}

// Notify sends a raised or resolved alert; failures are logged, never retried.
func (n *Notifier) Notify(ctx context.Context, settings domain.MonitoringAlertSettings, change service.AlertChange) {
	message, ok := n.noticeOf(change)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	for _, result := range n.send(ctx, settings, message) {
		if !result.OK {
			log.Printf("host monitor: %s notice of %s: %s", result.Channel, message.alert.ID, result.Error)
		}
	}
}

// Test sends a test notice to every configured channel.
func (n *Notifier) Test(ctx context.Context, settings domain.MonitoringAlertSettings) []ChannelResult {
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	text := "Test notice from Excalibase host monitoring: alerts will reach this channel."
	return n.send(ctx, settings, notice{event: "test", subject: "[Excalibase] Test notice", text: text})
}

func (n *Notifier) noticeOf(change service.AlertChange) (notice, bool) {
	switch {
	case change.Raised != nil:
		return notice{event: "alert.raised", subject: oneLine("[Excalibase] Alert: " + change.Raised.Message),
			text: n.describe("Alert", change.Raised), alert: change.Raised}, true
	case change.Resolved != nil:
		return notice{event: "alert.resolved", subject: oneLine("[Excalibase] Resolved: " + change.Resolved.Message),
			text: n.describe("Resolved", change.Resolved), alert: change.Resolved}, true
	}
	return notice{}, false
}

// oneLine keeps a subject one header line whatever a container is named.
func oneLine(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
}

func (n *Notifier) describe(kind string, alert *domain.Alert) string {
	lines := []string{kind + ": " + alert.Message}
	if alert.ProjectID != "" {
		lines = append(lines, "Project: "+alert.ProjectID)
	}
	if link := n.link(alert); link != "" {
		lines = append(lines, link)
	}
	return strings.Join(lines, "\n")
}

func (n *Notifier) link(alert *domain.Alert) string {
	if n.deps.StudioURL == "" {
		return ""
	}
	base := strings.TrimRight(n.deps.StudioURL, "/")
	if alert.ProjectID != "" {
		return base + "/project/" + url.PathEscape(alert.ProjectID) + "/monitoring/metrics"
	}
	return base + "/admin/host"
}

func (n *Notifier) send(ctx context.Context, settings domain.MonitoringAlertSettings, message notice) []ChannelResult {
	var results []ChannelResult
	if n.deps.Mail != nil && len(settings.Emails) > 0 {
		results = append(results, n.sendMail(ctx, settings.Emails, message))
	}
	if settings.WebhookDisplay != "" {
		results = append(results, n.sendWebhook(ctx, message))
	}
	return results
}

// sendMail writes to each recipient alone, so no one sees the others' addresses.
func (n *Notifier) sendMail(ctx context.Context, recipients []string, message notice) ChannelResult {
	result := ChannelResult{Channel: "email", OK: true}
	body := "<p>" + strings.ReplaceAll(html.EscapeString(message.text), "\n", "<br>") + "</p>"
	for _, recipient := range recipients {
		_, err := n.deps.Mail.Send(ctx, email.Message{To: []string{recipient}, From: n.deps.From, FromName: n.deps.FromName,
			Subject: message.subject, TextBody: message.text, HTMLBody: body, Tags: map[string]string{"kind": "host-alert"}})
		if err != nil {
			log.Printf("host monitor: email notice: %v", err)
			result.OK, result.Error = false, "the email could not be sent"
		}
	}
	return result
}

func (n *Notifier) sendWebhook(ctx context.Context, message notice) ChannelResult {
	result := ChannelResult{Channel: "webhook"}
	if n.deps.Vault == nil || n.deps.Vault.Sealed() {
		result.Error = "the vault is sealed"
		return result
	}
	entry, err := n.deps.Vault.Get(WebhookVaultPath)
	if err != nil || entry["url"] == "" {
		result.Error = "no webhook is stored"
		return result
	}
	// Slack, Mattermost, Rocket.Chat and Google Chat read "text".
	payload, _ := json.Marshal(map[string]any{"text": message.text, "event": message.event, "alert": webhookAlert(message.alert)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, entry["url"], bytes.NewReader(payload))
	if err != nil {
		result.Error = "the webhook URL is not usable"
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Excalibase-Monitoring")
	resp, err := n.client().Do(req)
	if err != nil {
		result.Error = "the webhook could not be reached"
		return result
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		result.Error = fmt.Sprintf("the webhook answered %d", resp.StatusCode)
		return result
	}
	result.OK = true
	return result
}

func (n *Notifier) client() *http.Client {
	if n.deps.Client != nil {
		return n.deps.Client
	}
	return GuardedClient()
}

func webhookAlert(alert *domain.Alert) map[string]any {
	if alert == nil {
		return nil
	}
	return map[string]any{
		"id": alert.ID, "projectId": alert.ProjectID, "severity": alert.Severity, "metric": alert.Metric,
		"message": alert.Message, "value": alert.Value, "threshold": alert.Threshold, "resolved": alert.Resolved,
		"timestamp": alert.Timestamp,
	}
}
