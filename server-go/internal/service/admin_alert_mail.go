package service

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

const adminMailTimeout = 15 * time.Second

// AdminEmails are the addresses a platform alert goes to.
type AdminEmails interface {
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
	Admins       AdminEmails
	Sender       email.Sender
	From         string
	FromName     string
	ProductName  string
	DashboardURL string
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// mailCooldown is how long a metric is quiet after its mail: a figure that
// flaps is one mail an hour, not one a minute.
const mailCooldown = time.Hour

// AdminAlertMail emails every platform admin when a platform alert is raised,
// through the platform's own mailer. A resolve is not mailed, and a metric is
// mailed at most once per mailCooldown.
type AdminAlertMail struct {
	config AdminAlertMailConfig
	mu     sync.Mutex
	last   map[string]time.Time
}

func NewAdminAlertMail(config AdminAlertMailConfig) *AdminAlertMail {
	if config.Now == nil {
		config.Now = time.Now
	}
	return &AdminAlertMail{config: config, last: map[string]time.Time{}}
}

// due records the mail and reports true unless the metric was mailed within the cooldown.
func (m *AdminAlertMail) due(metric string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.config.Now()
	if sent, ok := m.last[metric]; ok && now.Sub(sent) < mailCooldown {
		return false
	}
	m.last[metric] = now
	return true
}

// Notify mails a raise; failures are logged, never retried: the alert stays in the table.
func (m *AdminAlertMail) Notify(ctx context.Context, change AlertChange) {
	if change.Raised == nil || m.config.Sender == nil || m.config.Admins == nil || !m.due(change.Raised.Metric) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, adminMailTimeout)
	defer cancel()
	recipients, err := m.config.Admins.PlatformAdminEmails(ctx)
	if err != nil {
		log.Printf("admin alert mail: list admins: %v", err)
		return
	}
	message, err := email.BuildAlertEmail(email.AlertData{
		Title: change.Raised.Message, Severity: "high", Body: change.Raised.Message,
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
