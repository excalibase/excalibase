package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const adminMailTimeout = 15 * time.Second

// AdminEmailLister are the addresses a platform alert goes to.
type AdminEmailLister interface {
	PlatformAdminEmails(ctx context.Context) ([]string, error)
}

// PlatformAdminEmails are the people with the platform_admin role who have an
// verified address: never a service account, never a disabled one.
type PlatformAdminEmails struct{ Users storage.UserStore }

func (p PlatformAdminEmails) PlatformAdminEmails(ctx context.Context) ([]string, error) {
	users, err := p.Users.FindAllUsers(ctx)
	if err != nil {
		return nil, err
	}
	var emails []string
	for _, user := range users {
		if user.Role == "platform_admin" && user.Kind != domain.UserKindService && user.Active && user.EmailVerifiedAt != nil && user.Email != "" {
			emails = append(emails, user.Email)
		}
	}
	return emails, nil
}

// AdminAlertMailConfig wires AdminAlertMail.
type AdminAlertMailConfig struct {
	Admins       AdminEmailLister
	Sender       email.Sender
	From         string
	FromName     string
	ProductName  string
	DashboardURL string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// Batch is how long raises of one kind are collected into one mail; zero
	// mails each at once. Schedule runs the flush after it; time.AfterFunc when nil.
	Batch    time.Duration
	Schedule func(after time.Duration, flush func())
	// OpenAlerts reads, once per mail, the metrics of the platform alerts that
	// are still raised. A queued alert that has resolved by the time its mail
	// goes out is left out. Nil keeps every queued alert, and so does a failed
	// read: the queue is already cleared, so dropping on an error would lose them.
	OpenAlerts func(ctx context.Context) (map[string]bool, error)
}

// mailCooldown is how long a kind of alert is quiet after its mail: a figure
// that flaps is one mail an hour, not one a minute.
const mailCooldown = time.Hour

// AdminAlertMail emails every platform admin when a platform alert is raised,
// through the platform's own mailer. A resolve is not mailed. Alerts of one
// kind (the part of the metric before ":", so one per project of a kind) raised
// close together go out as one mail listing them, and a kind is mailed at most
// once per mailCooldown: one root cause hitting every project (EXC-773) is one
// mail, while each project keeps its own alert row. A raise inside the cooldown
// is not dropped: it waits for the next mail of its kind, sent when the hour is
// up. Mail still queued when the process exits is lost; the alert rows remain.
type AdminAlertMail struct {
	config    AdminAlertMailConfig
	mu        sync.Mutex
	last      map[string]time.Time
	pending   map[string][]domain.Alert
	scheduled map[string]bool
}

func NewAdminAlertMail(config AdminAlertMailConfig) *AdminAlertMail {
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Schedule == nil {
		config.Schedule = func(after time.Duration, flush func()) { time.AfterFunc(after, flush) }
	}
	return &AdminAlertMail{config: config, last: map[string]time.Time{}, pending: map[string][]domain.Alert{}, scheduled: map[string]bool{}}
}

// alertKind groups the per-project metrics of one alert ("backup.renewal:p1" and "backup.renewal:p2").
func alertKind(metric string) string {
	kind, _, _ := strings.Cut(metric, ":")
	return kind
}

// queue adds the alert to its kind's next mail, replacing an earlier raise of
// the same metric, and reports how long to wait before that mail goes out;
// ok is false when a mail for the kind is already scheduled.
func (m *AdminAlertMail) queue(alert domain.Alert) (wait time.Duration, ok bool) {
	kind := alertKind(alert.Metric)
	m.mu.Lock()
	defer m.mu.Unlock()
	queued := m.pending[kind]
	replaced := false
	for i := range queued {
		if queued[i].Metric == alert.Metric {
			queued[i], replaced = alert, true
		}
	}
	if !replaced {
		queued = append(queued, alert)
	}
	m.pending[kind] = queued
	if m.scheduled[kind] {
		return 0, false
	}
	m.scheduled[kind] = true
	wait = m.config.Batch
	if sent, was := m.last[kind]; was {
		wait = max(wait, mailCooldown-m.config.Now().Sub(sent))
	}
	return wait, true
}

// Notify queues a raise for mail; failures are logged, never retried: the alert stays in the table.
func (m *AdminAlertMail) Notify(ctx context.Context, change AlertChange) {
	if change.Raised == nil || m.config.Sender == nil || m.config.Admins == nil {
		return
	}
	wait, ok := m.queue(*change.Raised)
	if !ok {
		return
	}
	kind := alertKind(change.Raised.Metric)
	if wait <= 0 {
		m.flush(ctx, kind)
		return
	}
	m.config.Schedule(wait, func() { m.flush(context.Background(), kind) })
}

func (m *AdminAlertMail) flush(ctx context.Context, kind string) {
	m.mu.Lock()
	queued := m.pending[kind]
	delete(m.pending, kind)
	delete(m.scheduled, kind)
	m.mu.Unlock()
	alerts := m.stillOpen(ctx, queued)
	if len(alerts) == 0 {
		return
	}
	m.mu.Lock()
	m.last[kind] = m.config.Now()
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, adminMailTimeout)
	defer cancel()
	recipients, err := m.config.Admins.PlatformAdminEmails(ctx)
	if err != nil {
		log.Printf("admin alert mail: list admins: %v", err)
		return
	}
	title, body := alertMailText(kind, alerts)
	message, err := email.BuildAlertEmail(email.AlertData{
		Title: title, Severity: "high", Body: body,
		DashboardURL: m.config.DashboardURL, ProductName: m.config.ProductName,
	})
	if err != nil {
		log.Printf("admin alert mail: build: %v", err)
		return
	}
	message.From, message.FromName = m.config.From, m.config.FromName
	for _, recipient := range recipients {
		toOne := message
		toOne.To = []string{recipient}
		if _, err := m.config.Sender.Send(ctx, toOne); err != nil {
			log.Printf("admin alert mail: send: %v", err)
		}
	}
}

// stillOpen keeps the queued alerts that have not resolved while they waited,
// and all of them when that cannot be read.
func (m *AdminAlertMail) stillOpen(ctx context.Context, queued []domain.Alert) []domain.Alert {
	if m.config.OpenAlerts == nil {
		return queued
	}
	open, err := m.config.OpenAlerts(ctx)
	if err != nil {
		log.Printf("admin alert mail: read open alerts, mailing every queued alert: %v", err)
		return queued
	}
	var still []domain.Alert
	for _, alert := range queued {
		if open[alert.Metric] {
			still = append(still, alert)
		}
	}
	return still
}

// alertMailText is the one alert's own message, or a count and every message for several.
func alertMailText(kind string, alerts []domain.Alert) (title, body string) {
	if len(alerts) == 1 {
		return alerts[0].Message, alerts[0].Message
	}
	lines := make([]string, len(alerts))
	for i, alert := range alerts {
		lines[i] = alert.Message
	}
	return fmt.Sprintf("%d %s alerts", len(alerts), kind), strings.Join(lines, "\n")
}
