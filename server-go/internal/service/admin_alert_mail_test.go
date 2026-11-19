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

// A kind of alert that raises again inside the hour waits for the next mail: one mail an hour per kind,
// and nothing raised is lost. Another kind is not held back by it.
func TestAdminAlertMail_ARaiseInsideTheHourWaitsForTheNextMail(t *testing.T) {
	sender := &captureSender{}
	now := time.Now()
	var delays []time.Duration
	var flush []func()
	mailer := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{emails: []string{"a@x.io"}}, Sender: sender,
		Now: func() time.Time { return now }, Schedule: func(after time.Duration, f func()) { delays = append(delays, after); flush = append(flush, f) }})
	mailer.Notify(context.Background(), raised("first"))
	now = now.Add(5 * time.Minute)
	mailer.Notify(context.Background(), raised("flapped back"))
	if len(sender.sent) != 1 || len(flush) != 1 || delays[0] != 55*time.Minute {
		t.Fatalf("sent %d, scheduled %v; want 1 mail now and the next in 55m", len(sender.sent), delays)
	}
	other := raised("a pod is pending")
	other.Raised.Metric = "cluster.pending"
	mailer.Notify(context.Background(), other)
	if len(sender.sent) != 2 {
		t.Fatalf("a different kind was held back: %d mails", len(sender.sent))
	}
	now = now.Add(55 * time.Minute)
	flush[0]()
	if len(sender.sent) != 3 || !strings.Contains(sender.sent[2].TextBody, "flapped back") {
		t.Fatalf("the queued raise was not mailed: %d mails", len(sender.sent))
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

}

// EXC-784 review: p2 raised twenty minutes after p1 is not lost to the hourly limit. It goes out in the
// next mail of its kind, which is held until the hour is up.
func TestAdminAlertMail_ALaterProjectIsInTheNextMailOfItsKind(t *testing.T) {
	sender := &captureSender{}
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	var delays []time.Duration
	var flush []func()
	mailer := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{emails: []string{"a@x.io"}}, Sender: sender,
		Now: func() time.Time { return now }, Batch: time.Minute,
		Schedule: func(after time.Duration, f func()) { delays = append(delays, after); flush = append(flush, f) }})
	ctx := context.Background()

	mailer.Notify(ctx, raisedMetric("backup.renewal:p1", "credential of p1 could not be renewed"))
	now = now.Add(time.Minute)
	flush[0]()
	if len(sender.sent) != 1 || !strings.Contains(sender.sent[0].TextBody, "p1") {
		t.Fatalf("first mail: %+v", sender.sent)
	}

	now = now.Add(19 * time.Minute)
	mailer.Notify(ctx, raisedMetric("backup.renewal:p2", "credential of p2 could not be renewed"))
	if len(sender.sent) != 1 || len(flush) != 2 || delays[1] != 41*time.Minute {
		t.Fatalf("sent %d, delays %v; want p2 held 41 minutes to the end of the hour", len(sender.sent), delays)
	}
	mailer.Notify(ctx, raisedMetric("backup.renewal:p3", "credential of p3 could not be renewed"))
	if len(flush) != 2 {
		t.Fatal("a third raise scheduled a second digest")
	}
	now = now.Add(41 * time.Minute)
	flush[1]()
	if len(sender.sent) != 2 {
		t.Fatalf("sent %d mails, want 2", len(sender.sent))
	}
	next := sender.sent[1].TextBody
	if !strings.Contains(next, "p2") || !strings.Contains(next, "p3") || strings.Contains(next, "p1") {
		t.Fatalf("next mail = %q, want p2 and p3 only", next)
	}
}

type digestHarness struct {
	sender *captureSender
	now    *time.Time
	flush  *[]func()
	mailer *AdminAlertMail
}

// digestMailer is a mailer whose first mail has gone out, so what follows waits for the digest.
func digestMailer(t *testing.T, open func(context.Context) (map[string]bool, error)) digestHarness {
	t.Helper()
	sender := &captureSender{}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var flush []func()
	mailer := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{emails: []string{"a@x.io"}}, Sender: sender,
		Now: func() time.Time { return now }, OpenAlerts: open,
		Schedule: func(_ time.Duration, f func()) { flush = append(flush, f) }})
	mailer.Notify(context.Background(), raisedMetric("backup.renewal:p1", "credential of p1 could not be renewed"))
	if len(sender.sent) != 1 {
		t.Fatalf("setup: first mail not sent (%d)", len(sender.sent))
	}
	return digestHarness{sender, &now, &flush, mailer}
}

// Mutant: the replace of an earlier raise of the same metric. The same project raised twice in the
// cooldown is one line in the digest, with its latest message.
func TestAdminAlertMail_TheSameMetricRaisedTwiceIsListedOnce(t *testing.T) {
	h := digestMailer(t, nil)
	ctx := context.Background()
	*h.now = h.now.Add(10 * time.Minute)
	h.mailer.Notify(ctx, raisedMetric("backup.renewal:p2", "p2 failed at first"))
	h.mailer.Notify(ctx, raisedMetric("backup.renewal:p2", "p2 failed again"))
	h.mailer.Notify(ctx, raisedMetric("backup.renewal:p3", "p3 failed"))
	*h.now = h.now.Add(50 * time.Minute)
	(*h.flush)[0]()

	if len(h.sender.sent) != 2 {
		t.Fatalf("sent %d mails, want 2", len(h.sender.sent))
	}
	body := h.sender.sent[1].TextBody
	if strings.Count(body, "p2 failed") != 1 || !strings.Contains(body, "p2 failed again") || !strings.Contains(body, "p3 failed") {
		t.Fatalf("digest = %q, want p2 once with its latest message, and p3", body)
	}
	if !strings.Contains(h.sender.sent[1].Subject, "2 backup.renewal alerts") {
		t.Fatalf("subject = %q, want a count of 2", h.sender.sent[1].Subject)
	}
}

// A digest held for up to an hour must not report a problem that has gone.
func TestAdminAlertMail_ADelayedDigestDropsWhatHasResolved(t *testing.T) {
	open := map[string]bool{"backup.renewal:p1": true, "backup.renewal:p2": true, "backup.renewal:p3": true}
	h := digestMailer(t, func(context.Context) (map[string]bool, error) { return open, nil })
	ctx := context.Background()
	*h.now = h.now.Add(10 * time.Minute)
	h.mailer.Notify(ctx, raisedMetric("backup.renewal:p2", "p2 failed"))
	h.mailer.Notify(ctx, raisedMetric("backup.renewal:p3", "p3 failed"))
	open["backup.renewal:p2"] = false
	*h.now = h.now.Add(50 * time.Minute)
	(*h.flush)[0]()

	if len(h.sender.sent) != 2 {
		t.Fatalf("sent %d mails, want 2", len(h.sender.sent))
	}
	body := h.sender.sent[1].TextBody
	if strings.Contains(body, "p2") || !strings.Contains(body, "p3 failed") {
		t.Fatalf("digest = %q, want only the alert that is still open", body)
	}
	if h.sender.sent[1].Subject != "[high] p3 failed" {
		t.Fatalf("subject = %q, want the single remaining alert's own message", h.sender.sent[1].Subject)
	}
}

// When everything queued has resolved there is nothing to say: no mail, and the hour is not spent on it.
func TestAdminAlertMail_ADigestOfOnlyResolvedAlertsSendsNothing(t *testing.T) {
	open := map[string]bool{"backup.renewal:p1": true, "backup.renewal:p2": true}
	h := digestMailer(t, func(context.Context) (map[string]bool, error) { return open, nil })
	*h.now = h.now.Add(10 * time.Minute)
	h.mailer.Notify(context.Background(), raisedMetric("backup.renewal:p2", "p2 failed"))
	open["backup.renewal:p2"] = false
	*h.now = h.now.Add(50 * time.Minute)
	(*h.flush)[0]()
	if len(h.sender.sent) != 1 {
		t.Fatalf("sent %d mails, want only the first", len(h.sender.sent))
	}
	open["backup.renewal:p4"] = true
	h.mailer.Notify(context.Background(), raisedMetric("backup.renewal:p4", "p4 failed"))
	if len(h.sender.sent) != 2 || !strings.Contains(h.sender.sent[1].TextBody, "p4 failed") {
		t.Fatalf("a raise after an empty digest, with the hour up, was not mailed at once (%d mails)", len(h.sender.sent))
	}
}

// readFailingStore keeps no alerts and fails every read, as a platform DB that is down does.
type readFailingStore struct{}

func (readFailingStore) SaveAlert(context.Context, *domain.Alert) error { return nil }
func (readFailingStore) GetActiveAlerts(context.Context) ([]domain.Alert, error) {
	return nil, errors.New("platform db down")
}
func (readFailingStore) GetActiveAlertsForProject(context.Context, string) ([]domain.Alert, error) {
	return nil, errors.New("platform db down")
}
func (readFailingStore) GetAlertHistory(context.Context, int) ([]domain.Alert, error) {
	return nil, errors.New("platform db down")
}

// EXC-784 review: a failed read of the open alerts is not "nothing is open". The digest goes out with every
// queued alert, because the queue is already cleared and dropping them would lose them for good.
func TestAdminAlertMail_AFailedAlertReadStillMailsEveryQueuedAlert(t *testing.T) {
	alerts := NewAlertingService(t.TempDir())
	alerts.SetStore(readFailingStore{})
	sender := &captureSender{}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var flush []func()
	mailer := NewAdminAlertMail(AdminAlertMailConfig{Admins: fakeAdmins{emails: []string{"a@x.io"}}, Sender: sender,
		Now: func() time.Time { return now }, OpenAlerts: alerts.OpenPlatformMetrics,
		Schedule: func(_ time.Duration, f func()) { flush = append(flush, f) }})
	ctx := context.Background()
	mailer.Notify(ctx, raisedMetric("backup.renewal:p1", "p1 failed"))
	now = now.Add(10 * time.Minute)
	mailer.Notify(ctx, raisedMetric("backup.renewal:p2", "p2 failed"))
	mailer.Notify(ctx, raisedMetric("backup.renewal:p3", "p3 failed"))
	now = now.Add(50 * time.Minute)
	flush[0]()

	if len(sender.sent) != 2 {
		t.Fatalf("sent %d mails, want the digest to go out despite the failed read", len(sender.sent))
	}
	if body := sender.sent[1].TextBody; !strings.Contains(body, "p2 failed") || !strings.Contains(body, "p3 failed") {
		t.Fatalf("digest = %q, want every queued alert", body)
	}
}
