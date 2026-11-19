package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestPlatformAdminEmails_HumanAdminsWithAVerifiedAddressOnly(t *testing.T) {
	verified := time.Now()
	admins := PlatformAdminEmails{Users: allUsers{users: []*domain.User{
		{Email: "a@x.io", Role: "platform_admin", Kind: domain.UserKindHuman, Active: true, EmailVerifiedAt: &verified},
		{Email: "unverified@x.io", Role: "platform_admin", Kind: domain.UserKindHuman, Active: true},
		{Email: "svc@x.io", Role: "platform_admin", Kind: domain.UserKindService, Active: true, EmailVerifiedAt: &verified},
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

// A kind of alert that keeps flapping is one mail an hour, not one a minute; another kind is not held back by it.
func TestAdminAlertMail_AtMostOneMailPerKindPerHour(t *testing.T) {
	sender := &captureSender{}
	now := time.Now()
	mailer := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{emails: []string{"a@x.io"}}, Sender: sender,
		Now: func() time.Time { return now }})
	mailer.Notify(context.Background(), raised("first"))
	now = now.Add(5 * time.Minute)
	mailer.Notify(context.Background(), raised("flapped back"))
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d mails inside the hour, want 1", len(sender.sent))
	}
	other := raised("a pod is pending")
	other.Raised.Metric = "cluster.pending"
	mailer.Notify(context.Background(), other)
	if len(sender.sent) != 2 {
		t.Fatalf("a different kind was held back: %d mails", len(sender.sent))
	}
	now = now.Add(time.Hour)
	mailer.Notify(context.Background(), raised("still there an hour later"))
	if len(sender.sent) != 3 {
		t.Fatalf("no mail after the cooldown: %d", len(sender.sent))
	}
}

func raisedMetric(metric, message string) AlertChange {
	return AlertChange{Raised: &domain.Alert{ID: metric, Severity: "CRITICAL", Message: message, Metric: metric}}
}

// EXC-784 review: one root cause hit every project at once in EXC-773. That is one mail per kind of
// alert per hour, listing the projects, and the alert rows stay one per project.
func TestAdminAlertMail_OneMailPerPrefixListsEveryAffectedProject(t *testing.T) {
	sender := &captureSender{}
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	var flush []func()
	mailer := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{emails: []string{"a@x.io"}}, Sender: sender, From: "alerts@x.io",
		Now: func() time.Time { return now }, Batch: time.Minute, Schedule: func(_ time.Duration, f func()) { flush = append(flush, f) }})
	ctx := context.Background()

	for _, project := range []string{"p1", "p2", "p3"} {
		mailer.Notify(ctx, raisedMetric("backup.renewal:"+project, "credential of "+project+" could not be renewed"))
	}
	mailer.Notify(ctx, raisedMetric("wal.archiving:ns/p1", "database p1 cannot archive"))
	if len(sender.sent) != 0 || len(flush) != 2 {
		t.Fatalf("sent %d before the window closed, %d windows; want 0 and one per kind", len(sender.sent), len(flush))
	}
	for _, f := range flush {
		f()
	}
	if len(sender.sent) != 2 {
		t.Fatalf("sent %d mails, want one per kind", len(sender.sent))
	}
	var renewal string
	for _, message := range sender.sent {
		if strings.Contains(message.TextBody, "p1") && strings.Contains(message.TextBody, "p2") && strings.Contains(message.TextBody, "p3") {
			renewal = message.Subject
		}
	}
	if !strings.Contains(renewal, "3") {
		t.Fatalf("no mail listed all three projects (subject %q)", renewal)
	}

	flush = nil
	mailer.Notify(ctx, raisedMetric("backup.renewal:p4", "credential of p4 could not be renewed"))
	if len(flush) != 0 {
		t.Fatal("a second renewal mail was scheduled within the hour")
	}
	now = now.Add(time.Hour + time.Minute)
	mailer.Notify(ctx, raisedMetric("backup.renewal:p4", "credential of p4 could not be renewed"))
	if len(flush) != 1 {
		t.Fatal("no mail scheduled once the hour had passed")
	}
}
