package adminconsole

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/prometheus/client_golang/prometheus"
)

type captureNotifier struct {
	mu      sync.Mutex
	notices []SuspensionNotice
	err     error
	// hold, when set, keeps every send waiting until it is closed.
	hold chan struct{}
}

func (n *captureNotifier) Suspended(_ context.Context, notice SuspensionNotice) error {
	if n.hold != nil {
		<-n.hold
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notices = append(n.notices, notice)
	return n.err
}

func (n *captureNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.notices)
}

func noticeFixture(enabled bool) (*SuspensionService, *fakeSuspensions, *fakeControl, *captureNotifier) {
	svc, store, control, _ := newSuspensionFixture()
	store.owned["victim"][0].ProjectName = "Proxies"
	store.owned["victim"][2].ProjectName = "Shop"
	notifier := &captureNotifier{}
	svc.SetNotifier(notifier, func(context.Context) bool { return enabled })
	return svc, store, control, notifier
}

func TestSuspendSendsOneNoticeNamingWhatWasStopped(t *testing.T) {
	svc, store, _, notifier := noticeFixture(true)

	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Code: ReasonCryptoMining, Text: "xmrig in image"}); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if len(notifier.notices) != 1 {
		t.Fatalf("notices = %d, want exactly one", len(notifier.notices))
	}
	notice := notifier.notices[0]
	if notice.Email != "v@mailinator.test" || notice.Code != ReasonCryptoMining {
		t.Fatalf("notice = %+v", notice)
	}
	if len(notice.Projects) != 2 || notice.Projects[0].Name != "Proxies" || notice.Projects[0].Apps[0] != "proxy" || notice.Projects[1].Name != "Shop" {
		t.Fatalf("projects = %+v, want the two stopped apps under their project names", notice.Projects)
	}
	if store.codes["victim"] != ReasonCryptoMining {
		t.Fatalf("stored code = %q", store.codes["victim"])
	}
}

func TestSuspendNamesOnlyAppsThatStopped(t *testing.T) {
	svc, _, control, notifier := noticeFixture(true)
	control.failPause["a3"] = errors.New("cluster busy")

	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	projects := notifier.notices[0].Projects
	if len(projects) != 1 || projects[0].Name != "Proxies" {
		t.Fatalf("projects = %+v, the app that would not stop must not be listed", projects)
	}
}

func TestSuspendSendsNothingWhileTheFlagIsOff(t *testing.T) {
	svc, store, _, notifier := noticeFixture(false)

	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Code: ReasonProxyVPN, Text: "proxy"}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notices) != 0 {
		t.Fatalf("notices = %+v with the flag off", notifier.notices)
	}
	if store.users["victim"].Active {
		t.Fatal("the suspension itself must be unchanged")
	}
}

func TestSuspendHoldsWhenTheMailFails(t *testing.T) {
	svc, store, control, notifier := noticeFixture(true)
	notifier.err = errors.New("smtp down")

	result, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"})
	if err != nil {
		t.Fatalf("a mail failure must not fail the suspension: %v", err)
	}
	if store.users["victim"].Active || len(control.paused) != 2 || len(result.AppsStopped) != 2 {
		t.Fatalf("suspension must stand: %+v", result)
	}
}

// A mail that never went is retried by suspending again, once; after it went, nothing more.
func TestSuspendRetrySendsAFailedMailExactlyOnce(t *testing.T) {
	svc, store, _, notifier := noticeFixture(true)
	notifier.err = errors.New("smtp down")
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Code: ReasonCryptoMining, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if store.mailSent["victim"] {
		t.Fatal("a failed send must not be marked as sent")
	}

	notifier.err = nil
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Code: ReasonCryptoMining, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notices) != 2 || !store.mailSent["victim"] {
		t.Fatalf("notices = %d sent=%v, want the failed try and one retry", len(notifier.notices), store.mailSent["victim"])
	}
	retry := notifier.notices[1]
	if len(retry.Projects) != 2 || retry.Projects[0].Name != "Proxies" {
		t.Fatalf("the retry must still name what was stopped: %+v", retry.Projects)
	}

	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Code: ReasonCryptoMining, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notices) != 2 {
		t.Fatalf("notices = %d, a second retry sends nothing", len(notifier.notices))
	}
}

// Turning the flag on later must never mail about a suspension made while it was off.
func TestSuspendWithTheFlagOffOwesNoMailLater(t *testing.T) {
	svc, store, _, notifier := noticeFixture(false)
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if !store.mailSent["victim"] {
		t.Fatal("a suspension made with the flag off must be settled, not owed")
	}
	enabled := true
	svc.SetNotifier(notifier, func(context.Context) bool { return enabled })
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(notifier.notices) != 0 {
		t.Fatalf("notices = %d, the old suspension must stay unmailed", len(notifier.notices))
	}
}

// With no mail sender the mail is still owed when the flag is on.
func TestSuspendWithNoSenderLeavesTheMailOwed(t *testing.T) {
	svc, store, _, _ := noticeFixture(true)
	svc.SetNotifier(nil, func(context.Context) bool { return true })
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if store.mailSent["victim"] {
		t.Fatal("nothing could be sent, so the mail stays owed")
	}
}

// Two admins suspending at once produce one mail.
func TestConcurrentSuspendsSendOneMail(t *testing.T) {
	svc, _, _, notifier := noticeFixture(true)
	notifier.hold = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"})
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(notifier.hold)
	wg.Wait()
	if got := notifier.count(); got != 1 {
		t.Fatalf("notices = %d, want exactly one", got)
	}
}

var closing = Reason{Code: ReasonOther, Text: "org revoked: x", OrgClosed: true}

// A revoke suspends the owner but mails nothing then: the closure mail follows
// once the projects are gone.
func TestSuspendForAnOrgRevokeSendsNoSuspensionMail(t *testing.T) {
	svc, store, _, notifier := noticeFixture(true)
	if _, err := svc.Suspend(context.Background(), admin, "victim", closing); err != nil {
		t.Fatal(err)
	}
	if notifier.count() != 0 {
		t.Fatalf("notices = %d, the closure mail is sent after the teardown", notifier.count())
	}
	if !store.mailSent["victim"] {
		t.Fatal("the suspension mail must be settled, or a later suspend would send it")
	}
}

// The normal abuse flow: suspend first, revoke later. The closure mail is
// still owed, the earlier suspension mail does not stand in for it.
func TestClosureMailIsSentToAnOwnerAlreadySuspended(t *testing.T) {
	svc, _, _, notifier := noticeFixture(true)
	ctx := context.Background()
	if _, err := svc.Suspend(ctx, admin, "victim", Reason{Code: ReasonCryptoMining, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Suspend(ctx, admin, "victim", closing); err != nil {
		t.Fatal(err)
	}
	_ = svc.NotifyOrgClosed(ctx, "victim", "org-1")
	if notifier.count() != 2 || !notifier.notices[1].OrgClosed || notifier.notices[0].OrgClosed {
		t.Fatalf("notices = %+v, want the suspension mail then the closure mail", notifier.notices)
	}
}

func TestClosureMailIsSentOncePerOrg(t *testing.T) {
	svc, _, _, notifier := noticeFixture(true)
	ctx := context.Background()
	_ = svc.NotifyOrgClosed(ctx, "victim", "org-1")
	_ = svc.NotifyOrgClosed(ctx, "victim", "org-2")
	_ = svc.NotifyOrgClosed(ctx, "victim", "org-1")
	if notifier.count() != 2 {
		t.Fatalf("notices = %d, want one per closed org (2)", notifier.count())
	}
}

func TestConcurrentRevokesOfOneOrgSendOneClosureMail(t *testing.T) {
	svc, _, _, notifier := noticeFixture(true)
	notifier.hold = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.NotifyOrgClosed(context.Background(), "victim", "org-1")
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(notifier.hold)
	wg.Wait()
	if got := notifier.count(); got != 1 {
		t.Fatalf("notices = %d, want exactly one", got)
	}
}

func TestClosureMailIsOwedAgainAfterAFailedSend(t *testing.T) {
	svc, _, _, notifier := noticeFixture(true)
	notifier.err = errors.New("smtp down")
	if err := svc.NotifyOrgClosed(context.Background(), "victim", "org-1"); err == nil {
		t.Fatal("a failed send must be returned, so the revoke can keep the org for a retry")
	}
	notifier.err = nil
	if err := svc.NotifyOrgClosed(context.Background(), "victim", "org-1"); err != nil {
		t.Fatal(err)
	}
	if notifier.count() != 2 {
		t.Fatalf("notices = %d, a failed send must be retried by the next revoke", notifier.count())
	}
}

func TestClosureMailWithNoSenderIsNotAFailure(t *testing.T) {
	svc, _, _, _ := noticeFixture(true)
	svc.SetNotifier(nil, func(context.Context) bool { return true })
	if err := svc.NotifyOrgClosed(context.Background(), "victim", "org-1"); err != nil {
		t.Fatalf("no mail sender is not a failure: %v", err)
	}
}

func TestClosureMailWithTheFlagOffSendsNothing(t *testing.T) {
	svc, _, _, notifier := noticeFixture(false)
	if err := svc.NotifyOrgClosed(context.Background(), "victim", "org-1"); err != nil {
		t.Fatalf("flag off is not a failure: %v", err)
	}
	if notifier.count() != 0 {
		t.Fatalf("notices = %d with the flag off", notifier.count())
	}
}

func TestSuspendAgainDoesNotMailAgain(t *testing.T) {
	svc, _, _, notifier := noticeFixture(true)
	for i := 0; i < 2; i++ {
		if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(notifier.notices) != 1 {
		t.Fatalf("notices = %d, a repeat suspend of a suspended account is not a new event", len(notifier.notices))
	}
}

func TestSuspendDefaultsTheCodeToOtherAndRefusesAnUnknownOne(t *testing.T) {
	svc, store, _, _ := noticeFixture(true)
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Code: "because", Text: "x"}); !errors.Is(err, ErrReasonCodeUnknown) {
		t.Fatalf("err = %v, want ErrReasonCodeUnknown", err)
	}
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if store.codes["victim"] != ReasonOther {
		t.Fatalf("code = %q, want other", store.codes["victim"])
	}
}

var _ = apphost.StatusRunning

func mailCount(t *testing.T, outcome string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() != "excalibase_suspension_mail_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metric.GetLabel()[0].GetValue() == outcome {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func TestSuspendCountsMailOutcomes(t *testing.T) {
	failedBefore, sentBefore := mailCount(t, "failed"), mailCount(t, "sent")
	svc, _, _, notifier := noticeFixture(true)
	notifier.err = errors.New("smtp down")
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := mailCount(t, "failed") - failedBefore; got != 1 {
		t.Fatalf("failed mails rose by %v, want 1", got)
	}
	svc2, _, _, _ := noticeFixture(true)
	if _, err := svc2.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := mailCount(t, "sent") - sentBefore; got != 1 {
		t.Fatalf("sent mails rose by %v, want 1", got)
	}
}

// An owner the store cannot find, or one with no address, is owed nothing: the
// revoke must still be able to finish.
func TestClosureMailForAnOwnerWithNoAddressOrNoAccountIsNotAnError(t *testing.T) {
	svc, store, _, notifier := noticeFixture(true)
	if err := svc.NotifyOrgClosed(context.Background(), "ghost", "org-1"); err != nil {
		t.Fatalf("missing owner: %v", err)
	}
	store.users["victim"].Email = ""
	if err := svc.NotifyOrgClosed(context.Background(), "victim", "org-1"); err != nil {
		t.Fatalf("empty email: %v", err)
	}
	if notifier.count() != 0 {
		t.Fatalf("notices = %d, nothing is owed", notifier.count())
	}
}

// While one revoke's send is in flight, another must not finish the job: if the
// send fails the org row is what a retry needs.
func TestAClosureSendInFlightMakesAConcurrentRevokeWait(t *testing.T) {
	svc, _, _, notifier := noticeFixture(true)
	notifier.hold = make(chan struct{})
	first := make(chan error, 1)
	go func() { first <- svc.NotifyOrgClosed(context.Background(), "victim", "org-1") }()
	time.Sleep(50 * time.Millisecond)
	if err := svc.NotifyOrgClosed(context.Background(), "victim", "org-1"); !errors.Is(err, ErrClosureNoticeInFlight) {
		t.Fatalf("second revoke got %v, want ErrClosureNoticeInFlight", err)
	}
	close(notifier.hold)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := svc.NotifyOrgClosed(context.Background(), "victim", "org-1"); err != nil {
		t.Fatalf("after the send succeeded a revoke may finish: %v", err)
	}
}
