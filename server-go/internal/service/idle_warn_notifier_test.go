package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
)

type fakeUserStore struct {
	users map[string]*domain.User
}

func (f *fakeUserStore) CreateUser(context.Context, *domain.User) error { return nil }
func (f *fakeUserStore) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	return f.users[id], nil
}
func (f *fakeUserStore) FindUserByUsername(context.Context, string) (*domain.User, error) {
	return nil, nil
}
func (f *fakeUserStore) FindUserByEmail(context.Context, string) (*domain.User, error) {
	return nil, nil
}
func (f *fakeUserStore) FindAllUsers(context.Context) ([]*domain.User, error)     { return nil, nil }
func (f *fakeUserStore) DeleteUser(context.Context, string) error                 { return nil }
func (f *fakeUserStore) UpdateUserPassword(context.Context, string, string) error { return nil }

type captureSender struct {
	sent []email.Message
}

func (c *captureSender) Send(_ context.Context, msg email.Message) (string, error) {
	c.sent = append(c.sent, msg)
	return "msg-1", nil
}

func idleInstance() *domain.DatabaseInstance {
	return &domain.DatabaseInstance{ProjectID: "proj_1", ProjectName: "Shop API", OwnerID: "u1"}
}

func TestIdleWarnEmail_SendsToOwner(t *testing.T) {
	sender := &captureSender{}
	users := &fakeUserStore{users: map[string]*domain.User{"u1": {ID: "u1", Email: "owner@example.com"}}}
	notifier := NewIdleWarnEmail(IdleWarnEmailConfig{Users: users, Sender: sender, ProductName: "Excalibase", DashboardURL: "https://app.example.com"})
	pauseAt := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)

	if err := notifier.NotifyIdleWarning(context.Background(), idleInstance(), pauseAt); err != nil {
		t.Fatalf("NotifyIdleWarning: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d messages", len(sender.sent))
	}
	msg := sender.sent[0]
	if len(msg.To) != 1 || msg.To[0] != "owner@example.com" {
		t.Errorf("recipient: %v", msg.To)
	}
	if !strings.Contains(msg.Subject, "Shop API") || !strings.Contains(msg.TextBody, "22 Sep 2026") {
		t.Errorf("subject/body: %q / %q", msg.Subject, msg.TextBody)
	}
	if msg.Tags["category"] != "idle-pause-warning" {
		t.Errorf("tags: %v", msg.Tags)
	}
}

func TestIdleWarnEmail_NoProviderReportsNotConfigured(t *testing.T) {
	users := &fakeUserStore{users: map[string]*domain.User{"u1": {ID: "u1", Email: "owner@example.com"}}}
	notifier := NewIdleWarnEmail(IdleWarnEmailConfig{Users: users, Sender: email.NewNoopSender()})
	err := notifier.NotifyIdleWarning(context.Background(), idleInstance(), time.Now())
	if !errors.Is(err, email.ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured, got %v", err)
	}
}

func TestIdleWarnEmail_OwnerWithoutEmailFails(t *testing.T) {
	users := &fakeUserStore{users: map[string]*domain.User{}}
	notifier := NewIdleWarnEmail(IdleWarnEmailConfig{Users: users, Sender: &captureSender{}})
	if err := notifier.NotifyIdleWarning(context.Background(), idleInstance(), time.Now()); err == nil {
		t.Error("expected an error when the owner cannot be resolved")
	}
}
