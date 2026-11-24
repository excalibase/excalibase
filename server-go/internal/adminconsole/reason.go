package adminconsole

import (
	"context"
	"errors"
	"log"

	"github.com/excalibase/provisioning-poc/internal/metrics"
)

// ReasonCode is the rule a suspension is for. It picks the email the account
// holder gets (EXC-841), so the set is closed.
type ReasonCode string

const (
	ReasonProxyVPN        ReasonCode = "proxy_vpn"
	ReasonCryptoMining    ReasonCode = "crypto_mining"
	ReasonSpamOrPhishing  ReasonCode = "spam_or_phishing"
	ReasonDisposableEmail ReasonCode = "disposable_email"
	ReasonOther           ReasonCode = "other"
)

// ErrReasonCodeUnknown is a code outside the set above.
var ErrReasonCodeUnknown = errors.New("unknown reason code: use proxy_vpn, crypto_mining, spam_or_phishing, disposable_email or other")

// Reason is why an admin suspends an account: the rule, and the written note
// every admin action carries.
type Reason struct {
	Code ReasonCode
	Text string
	// OrgClosed is set only by an org revoke, which deletes the organisation:
	// its mail says so. A request cannot set it.
	OrgClosed bool
}

// ParseReasonCode reads a code from a request; an empty one is "other".
func ParseReasonCode(raw string) (ReasonCode, error) {
	code := ReasonCode(raw)
	switch code {
	case "":
		return ReasonOther, nil
	case ReasonProxyVPN, ReasonCryptoMining, ReasonSpamOrPhishing, ReasonDisposableEmail, ReasonOther:
		return code, nil
	}
	return "", ErrReasonCodeUnknown
}

// NoticeProject is a project whose apps a suspension stopped.
type NoticeProject struct {
	Name string
	Apps []string
}

// SuspensionNotice is what the account holder is told: the rule and what was
// stopped, nothing else.
type SuspensionNotice struct {
	Email    string
	Code     ReasonCode
	Projects []NoticeProject
	// OrgClosed: the organisation is being deleted, not only suspended.
	OrgClosed bool
}

// SuspensionNotifier tells the account holder about a suspension.
type SuspensionNotifier interface {
	Suspended(ctx context.Context, notice SuspensionNotice) error
}

// SetNotifier mails the account holder after each new suspension while
// enabled says so (FEATURE_SUSPENSIONEMAILS), decided per suspension.
func (s *SuspensionService) SetNotifier(notifier SuspensionNotifier, enabled func(context.Context) bool) {
	s.notifier, s.mailEnabled = notifier, enabled
}

// notify sends the one mail while the flag is on. The mail is claimed before
// it is sent, so two suspends at once send one, and given back if the send
// fails, so suspending again retries it. With the flag off the mail is settled
// at once: turning the flag on later never mails about an older suspension.
// A failure is logged and counted, never a reason to undo the suspension.
func (s *SuspensionService) notify(ctx context.Context, user *UserRef, why Reason, code ReasonCode, failed []AppFailure) {
	if s.mailEnabled == nil || user.MailSent {
		return
	}
	if why.OrgClosed {
		// A revoke's mail is the closure mail, sent once the projects are
		// gone (NotifyOrgClosed). The suspension mail is settled unsent, or a
		// later suspend would send a notice about this one.
		if _, err := s.store.ClaimMail(ctx, user.ID); err != nil {
			log.Printf("ERROR: settle suspension mail for user %s: %v", user.ID, err)
		}
		return
	}
	if !s.mailEnabled(ctx) {
		if _, err := s.store.ClaimMail(ctx, user.ID); err != nil {
			log.Printf("ERROR: settle suspension mail for user %s: %v", user.ID, err)
		}
		return
	}
	if s.notifier == nil {
		return
	}
	recorded, err := s.store.SuspendedApps(ctx, user.ID)
	if err != nil {
		log.Printf("ERROR: suspension mail for user %s: list stopped apps: %v", user.ID, err)
		metrics.SuspensionMail("failed")
		return
	}
	claimed, err := s.store.ClaimMail(ctx, user.ID)
	if err != nil {
		log.Printf("ERROR: suspension mail for user %s: claim: %v", user.ID, err)
		metrics.SuspensionMail("failed")
		return
	}
	if !claimed {
		return
	}
	notice := SuspensionNotice{Email: user.Email, Code: code, Projects: noticeProjects(withoutFailed(recorded, failed))}
	if err := s.notifier.Suspended(ctx, notice); err != nil {
		log.Printf("ERROR: suspension mail for user %s: %v", user.ID, err)
		metrics.SuspensionMail("failed")
		if err := s.store.ReleaseMail(ctx, user.ID); err != nil {
			log.Printf("ERROR: suspension mail for user %s not released for a retry: %v", user.ID, err)
		}
		return
	}
	metrics.SuspensionMail("sent")
}

// NotifyOrgClosed tells an org's owner the org was closed and its data is being
// deleted, once per (owner, org): the claim is per org, so it does not depend on
// whether the owner was suspended before, and revoking a second org mails again.
// The caller sends it only after the teardown succeeded. A failed send gives the
// claim back, so the next revoke of the org sends it. Failures are logged and
// counted, never returned: the deletion is not undone for a mail.
func (s *SuspensionService) NotifyOrgClosed(ctx context.Context, userID, orgID string) {
	if s.mailEnabled == nil || s.notifier == nil || !s.mailEnabled(ctx) {
		return
	}
	user, err := s.store.FindUser(ctx, userID)
	if err != nil || user == nil {
		log.Printf("ERROR: closure mail for user %s org %s: look up user: %v", userID, orgID, err)
		metrics.SuspensionMail("failed")
		return
	}
	claimed, err := s.store.ClaimClosureMail(ctx, userID, orgID)
	if err != nil {
		log.Printf("ERROR: closure mail for user %s org %s: claim: %v", userID, orgID, err)
		metrics.SuspensionMail("failed")
		return
	}
	if !claimed {
		return
	}
	if err := s.notifier.Suspended(ctx, SuspensionNotice{Email: user.Email, Code: ReasonOther, OrgClosed: true}); err != nil {
		log.Printf("ERROR: closure mail for user %s org %s: %v", userID, orgID, err)
		metrics.SuspensionMail("failed")
		if err := s.store.ReleaseClosureMail(ctx, userID, orgID); err != nil {
			log.Printf("ERROR: closure mail for user %s org %s not released for a retry: %v", userID, orgID, err)
		}
		return
	}
	metrics.SuspensionMail("sent")
}

// withoutFailed drops the apps this run could not stop; the mail names only what stopped.
func withoutFailed(apps []SuspendedApp, failed []AppFailure) []SuspendedApp {
	out := []SuspendedApp{}
	for _, app := range apps {
		stuck := false
		for _, f := range failed {
			stuck = stuck || (f.ProjectID == app.ProjectID && f.AppID == app.AppID)
		}
		if !stuck {
			out = append(out, app)
		}
	}
	return out
}

// noticeProjects groups the stopped apps under their project, in the order they were stopped.
func noticeProjects(stopped []SuspendedApp) []NoticeProject {
	out := []NoticeProject{}
	index := map[string]int{}
	for _, app := range stopped {
		name := app.ProjectName
		if name == "" {
			name = app.ProjectID
		}
		at, seen := index[app.ProjectID]
		if !seen {
			at = len(out)
			index[app.ProjectID] = at
			out = append(out, NoticeProject{Name: name})
		}
		out[at].Apps = append(out[at].Apps, app.AppName)
	}
	return out
}
