//go:build integration

package email

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestResendSender_Live sends a real email via Resend's API. Gated on
// RESEND_API_KEY so it never runs in default `make test`. Send target
// is `delivered@resend.dev` — Resend's documented test recipient that
// always reports as delivered without bouncing real inboxes.
//
// Verifies what unit tests can't: the API key has the expected scope,
// the SDK's payload shape matches what Resend accepts today, and the
// returned message ID is well-formed.
func TestResendSender_Live(t *testing.T) {
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		t.Skip("RESEND_API_KEY not set — export it to run the live test")
	}
	from := os.Getenv("RESEND_TEST_FROM")
	if from == "" {
		// Resend's onboarding sender accepts any project's API key in
		// dev. Real production traffic would set RESEND_TEST_FROM to a
		// verified domain.
		from = "onboarding@resend.dev"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sender, err := NewResendSender(ResendConfig{
		APIKey:          apiKey,
		DefaultFrom:     from,
		DefaultFromName: "Excalibase Test",
		SendsPerSecond:  2,
	})
	if err != nil {
		t.Fatalf("NewResendSender: %v", err)
	}
	t.Cleanup(sender.Stop)

	to := os.Getenv("RESEND_TEST_TO")
	if to == "" {
		// Default: Resend's documented test sink — always reports as
		// delivered, never bounces real inboxes. Override RESEND_TEST_TO
		// to send to your own verified address.
		to = "delivered@resend.dev"
	}
	id, err := sender.Send(ctx, Message{
		To:       []string{to},
		Subject:  "[excalibase] live integration test",
		HTMLBody: "<p>Sent from <code>resend_live_test.go</code> at " + time.Now().UTC().Format(time.RFC3339) + "</p>",
		TextBody: "Sent from resend_live_test.go at " + time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id == "" {
		t.Errorf("expected non-empty message id")
	}
	t.Logf("resend message id: %s", id)
}
