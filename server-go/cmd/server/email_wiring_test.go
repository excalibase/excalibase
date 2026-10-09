package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/email"
)

// EMAIL_PROVIDER=smtp yields a real sender; a relay setting the sender
// refuses stops the boot instead of degrading every sign-up to a 503.
func TestBuildSMTPSender(t *testing.T) {
	sender, err := buildSMTPSender(config.AppConfig{
		EmailProvider: "smtp", SMTPHost: "mail.internal", EmailFromAddress: "noreply@self.test",
	})
	if err != nil {
		t.Fatalf("buildSMTPSender: %v", err)
	}
	if !email.Configured(sender) {
		t.Error("a configured relay produced a no-op sender")
	}

	if _, err := buildSMTPSender(config.AppConfig{
		EmailProvider: "smtp", SMTPHost: "mail.internal", EmailFromAddress: "noreply@self.test", SMTPTLS: "sometimes",
	}); err == nil {
		t.Error("an unknown SMTP_TLS mode was accepted")
	}
	if _, err := buildSMTPSender(config.AppConfig{
		EmailProvider: "smtp", SMTPHost: "mail.internal", EmailFromAddress: "noreply@self.test",
		SMTPTLS: "none", SMTPUsername: "u", SMTPPassword: "p",
	}); err == nil {
		t.Error("credentials over a plain connection were accepted")
	}
}
