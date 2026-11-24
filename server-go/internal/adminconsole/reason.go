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

// notify sends the one mail. A failure is logged and counted, never a reason
// to undo a suspension that is already in force.
func (s *SuspensionService) notify(ctx context.Context, user *UserRef, code ReasonCode, stopped []SuspendedApp) {
	if s.notifier == nil || s.mailEnabled == nil || !s.mailEnabled(ctx) || user.Suspended {
		return
	}
	notice := SuspensionNotice{Email: user.Email, Code: code, Projects: noticeProjects(stopped)}
	if err := s.notifier.Suspended(ctx, notice); err != nil {
		log.Printf("ERROR: suspension mail for user %s: %v", user.ID, err)
		metrics.SuspensionMail("failed")
		return
	}
	metrics.SuspensionMail("sent")
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
