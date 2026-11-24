package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/email"
)

// SuspensionMailConfig is what the mail needs from configuration.
type SuspensionMailConfig struct {
	From, FromName string
	// SupportEmail is where an appeal is sent and the mail's reply-to.
	SupportEmail string
	// PolicyURL is the Acceptable Use Policy page; each rule is an anchor on it.
	PolicyURL string
}

// SuspensionMail tells an account holder their account was suspended, which
// rule it broke and how to appeal (EXC-841). It is not the crash-loop mail:
// an app that kept crashing is not abuse and has its own message.
type SuspensionMail struct {
	sender email.Sender
	cfg    SuspensionMailConfig
}

func NewSuspensionMail(sender email.Sender, cfg SuspensionMailConfig) *SuspensionMail {
	cfg.PolicyURL = strings.TrimRight(cfg.PolicyURL, "/")
	return &SuspensionMail{sender: sender, cfg: cfg}
}

// suspensionRules is the plain-words rule each reason code names. The anchor
// on the policy page is the code itself.
var suspensionRules = map[adminconsole.ReasonCode]struct{ subject, rule string }{
	adminconsole.ReasonProxyVPN: {
		"Your Excalibase account was suspended: proxy or VPN service",
		"Running a proxy, VPN or tunnel service for other people is not allowed on Excalibase.",
	},
	adminconsole.ReasonCryptoMining: {
		"Your Excalibase account was suspended: cryptocurrency mining",
		"Cryptocurrency mining is not allowed on Excalibase.",
	},
	adminconsole.ReasonSpamOrPhishing: {
		"Your Excalibase account was suspended: spam or phishing",
		"Sending spam, or running phishing or scam pages, is not allowed on Excalibase.",
	},
	adminconsole.ReasonDisposableEmail: {
		"Your Excalibase account was suspended: temporary email address",
		"Accounts created with a temporary or disposable email address are not allowed on Excalibase.",
	},
	adminconsole.ReasonOther: {
		"Your Excalibase account was suspended",
		"We found use of your account that breaks our Acceptable Use Policy.",
	},
}

// Suspended mails the account holder; the failure is the caller's to log and count.
func (m *SuspensionMail) Suspended(ctx context.Context, notice adminconsole.SuspensionNotice) error {
	if notice.Email == "" {
		return errors.New("the account has no email address")
	}
	rule, known := suspensionRules[notice.Code]
	if !known {
		rule = suspensionRules[adminconsole.ReasonOther]
		notice.Code = adminconsole.ReasonOther
	}
	ctx, cancel := context.WithTimeout(ctx, adminMailTimeout)
	defer cancel()
	msg := email.Message{
		To: []string{notice.Email}, From: m.cfg.From, FromName: m.cfg.FromName, ReplyTo: m.cfg.SupportEmail,
		Subject: rule.subject, TextBody: m.body(rule.rule, notice),
		Tags: map[string]string{"kind": "account-suspended", "reason": string(notice.Code)},
	}
	if _, err := m.sender.Send(ctx, msg); err != nil {
		return fmt.Errorf("send suspension mail: %w", err)
	}
	return nil
}

func (m *SuspensionMail) body(rule string, notice adminconsole.SuspensionNotice) string {
	var b strings.Builder
	b.WriteString("Hello,\n\nWe have suspended your Excalibase account, so you cannot sign in for now.\n\n")
	b.WriteString("Why\n" + rule + "\n")
	if m.cfg.PolicyURL != "" {
		fmt.Fprintf(&b, "The rule is in our Acceptable Use Policy: %s#%s\n", m.cfg.PolicyURL, notice.Code)
	}
	b.WriteString("\nWhat we stopped\n")
	if len(notice.Projects) == 0 {
		b.WriteString("Your account is deactivated. No apps were running, so none were stopped.\n")
	}
	for _, project := range notice.Projects {
		fmt.Fprintf(&b, "- Project %q: %s\n", project.Name, quotedList(project.Apps))
	}
	b.WriteString("\nYour data\nWe have not deleted your data. Your databases, files and backups are kept as they are while your account is suspended.\n")
	b.WriteString("\nIf you think this is a mistake\n")
	if m.cfg.SupportEmail != "" {
		fmt.Fprintf(&b, "Reply to this email or write to %s. Tell us what your apps do and we will review the suspension.\n", m.cfg.SupportEmail)
	} else {
		b.WriteString("Reply to this email and tell us what your apps do. We will review the suspension.\n")
	}
	b.WriteString("\nThe Excalibase team\n")
	return b.String()
}

func quotedList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return strings.Join(quoted, ", ")
}
