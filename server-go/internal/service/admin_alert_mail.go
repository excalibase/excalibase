package service

import (
	"context"
	"log"
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
// address: never a service account, never a disabled one.
type PlatformAdminEmails struct{ Users storage.UserStore }

func (p PlatformAdminEmails) PlatformAdminEmails(ctx context.Context) ([]string, error) {
	users, err := p.Users.FindAllUsers(ctx)
	if err != nil {
		return nil, err
	}
	var emails []string
	for _, user := range users {
		if user.Role == "platform_admin" && user.Kind != domain.UserKindService && user.Active && user.Email != "" {
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
}

// AdminAlertMail emails every platform admin when a platform alert is raised,
// through the platform's own mailer. A resolve is not mailed.
type AdminAlertMail struct{ config AdminAlertMailConfig }

func NewAdminAlertMail(config AdminAlertMailConfig) *AdminAlertMail {
	return &AdminAlertMail{config: config}
}

// Notify mails a raise; failures are logged, never retried: the alert stays in the table.
func (m *AdminAlertMail) Notify(ctx context.Context, change AlertChange) {
	if change.Raised == nil || m.config.Sender == nil || m.config.Admins == nil {
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
