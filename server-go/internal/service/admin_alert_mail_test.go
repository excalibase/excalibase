package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

type fakeAdmins struct {
	emails []string
	err    error
}

func (f fakeAdmins) PlatformAdminEmails(context.Context) ([]string, error) { return f.emails, f.err }

type allUsers struct {
	*fakeUserStore
	users []*domain.User
}

func (a allUsers) FindAllUsers(context.Context) ([]*domain.User, error) { return a.users, nil }

func TestPlatformAdminEmails_HumanAdminsWithAnAddressOnly(t *testing.T) {
	admins := PlatformAdminEmails{Users: allUsers{users: []*domain.User{
		{Email: "a@x.io", Role: "platform_admin", Kind: domain.UserKindHuman, Active: true},
		{Email: "svc@x.io", Role: "platform_admin", Kind: domain.UserKindService, Active: true},
		{Email: "m@x.io", Role: "member", Kind: domain.UserKindHuman, Active: true},
		{Email: "", Role: "platform_admin", Kind: domain.UserKindHuman, Active: true},
		{Email: "off@x.io", Role: "platform_admin", Kind: domain.UserKindHuman, Active: false},
	}}}
	got, err := admins.PlatformAdminEmails(context.Background())
	if err != nil || len(got) != 1 || got[0] != "a@x.io" {
		t.Fatalf("emails = %v, %v", got, err)
	}
}

func raised(message string) AlertChange {
	return AlertChange{Raised: &domain.Alert{ID: "a1", Severity: "WARNING", Message: message, Metric: "cluster.pods:n1"}}
}

func TestAdminAlertMail_EmailsEveryPlatformAdminOnARaise(t *testing.T) {
	sender := &captureSender{}
	mailer := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{emails: []string{"a@x.io", "b@x.io"}}, Sender: sender,
		From: "alerts@x.io", FromName: "Excalibase", DashboardURL: "https://studio.x.io"})
	mailer.Notify(context.Background(), raised("Node n1 runs 190 of 200 pods"))
	if len(sender.sent) != 2 || sender.sent[0].To[0] != "a@x.io" || sender.sent[1].To[0] != "b@x.io" {
		t.Fatalf("sent = %+v, want one mail per admin, each to one address", sender.sent)
	}
	if !strings.Contains(sender.sent[0].Subject, "Node n1 runs 190 of 200 pods") || sender.sent[0].From != "alerts@x.io" {
		t.Errorf("message = %+v", sender.sent[0])
	}
}

func TestAdminAlertMail_IgnoresAResolveAndSurvivesAFailingLookup(t *testing.T) {
	sender := &captureSender{}
	mailer := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{emails: []string{"a@x.io"}}, Sender: sender})
	mailer.Notify(context.Background(), AlertChange{Resolved: &domain.Alert{Message: "x"}})
	if len(sender.sent) != 0 {
		t.Fatal("a resolve was mailed")
	}
	failing := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{err: errors.New("db down")}, Sender: sender})
	failing.Notify(context.Background(), raised("x"))
	if len(sender.sent) != 0 {
		t.Fatal("mailed without a recipient list")
	}
}
