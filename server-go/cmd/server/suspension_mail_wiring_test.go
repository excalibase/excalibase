package main

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/email"
)

func TestSuspensionNotifier_NeedsARealMailSender(t *testing.T) {
	cfg := config.AppConfig{EmailFromAddress: "noreply@example.test", SupportEmail: "support@example.test", AcceptableUseURL: "https://example.test/acceptable-use"}
	if suspensionNotifier(cfg, nil) != nil || suspensionNotifier(cfg, email.NewNoopSender()) != nil {
		t.Fatal("without a mail sender there is no notifier: the suspension stands, nothing is mailed")
	}
	if suspensionNotifier(cfg, liveSender{}) == nil {
		t.Fatal("a configured sender gets a notifier")
	}
}
