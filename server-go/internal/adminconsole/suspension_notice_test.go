package adminconsole

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/prometheus/client_golang/prometheus"
)

type captureNotifier struct {
	notices []SuspensionNotice
	err     error
}

func (n *captureNotifier) Suspended(_ context.Context, notice SuspensionNotice) error {
	n.notices = append(n.notices, notice)
	return n.err
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

func TestSuspendWithTheFlagOffLeavesTheMailOwed(t *testing.T) {
	svc, store, _, _ := noticeFixture(false)
	if _, err := svc.Suspend(context.Background(), admin, "victim", Reason{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if store.mailSent["victim"] {
		t.Fatal("nothing was sent, so nothing is marked sent")
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
