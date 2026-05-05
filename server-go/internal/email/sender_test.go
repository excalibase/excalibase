package email

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const testEmailAddr = "u@example.com"


// fakeSender records the last call so handler tests can assert what was
// dispatched without needing a live SES account. Lives in the package so
// it can use unexported helpers if needed later.
type fakeSender struct {
	last     Message
	callCount int
	returnErr error
}

func (f *fakeSender) Send(ctx context.Context, msg Message) (string, error) {
	f.last = msg
	f.callCount++
	if f.returnErr != nil {
		return "", f.returnErr
	}
	return "fake-msg-id", nil
}

func TestRender_HTMLAndText(t *testing.T) {
	htmlTpl := "<p>Hello {{.Name}}</p>"
	textTpl := "Hello {{.Name}}"
	html, text, err := Render(htmlTpl, textTpl, struct{ Name string }{"world"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(html, "Hello world") {
		t.Errorf("html: %s", html)
	}
	if text != "Hello world" {
		t.Errorf("text: %q", text)
	}
}

func TestBuildVerifyEmail_SetsDefaults(t *testing.T) {
	msg, err := BuildVerifyEmail(VerifyEmailData{
		UserEmail: "user@example.com",
		VerifyURL: "https://app.example.com/v/abc",
	})
	if err != nil {
		t.Fatalf("BuildVerifyEmail: %v", err)
	}
	if msg.Subject != "Verify your email" {
		t.Errorf("subject: %q", msg.Subject)
	}
	if !strings.Contains(msg.HTMLBody, "Excalibase") {
		t.Errorf("default product name not applied: %s", msg.HTMLBody)
	}
	if !strings.Contains(msg.HTMLBody, "24 hours") {
		t.Errorf("default expiry not applied: %s", msg.HTMLBody)
	}
	if !strings.Contains(msg.HTMLBody, "https://app.example.com/v/abc") {
		t.Errorf("verify URL missing from body")
	}
	if msg.Tags["template"] != "verify_email" {
		t.Errorf("template tag: %v", msg.Tags)
	}
}

func TestBuildPasswordResetEmail_IncludesIPWhenSet(t *testing.T) {
	msg, _ := BuildPasswordResetEmail(PasswordResetData{
		UserEmail: testEmailAddr,
		ResetURL:  "https://app/r/abc",
		IPAddress: "1.2.3.4",
	})
	if !strings.Contains(msg.HTMLBody, "1.2.3.4") {
		t.Errorf("IP not in body when provided: %s", msg.HTMLBody)
	}

	msg2, _ := BuildPasswordResetEmail(PasswordResetData{
		UserEmail: testEmailAddr,
		ResetURL:  "https://app/r/abc",
	})
	// When IP is empty the {{if .IPAddress}} branch should NOT render the
	// "Requested from" suffix. Stricter than just absence of "1.2.3.4".
	if strings.Contains(msg2.HTMLBody, "Requested from") {
		t.Errorf("body should not mention IP when empty: %s", msg2.HTMLBody)
	}
}

func TestValidateRecipients_RejectsBadAddresses(t *testing.T) {
	cases := [][]string{
		{},
		{""},
		{"not-an-email"},
		{"@no-local"},
		{"no-domain@"},
		{"valid@example.com", "@oops"},
	}
	for _, c := range cases {
		if err := validateRecipients(c); !errors.Is(err, ErrInvalidRecipient) {
			t.Errorf("expected ErrInvalidRecipient for %v, got %v", c, err)
		}
	}
	if err := validateRecipients([]string{"good@example.com"}); err != nil {
		t.Errorf("good address rejected: %v", err)
	}
}

func TestRateLimiter_BlocksAndRefills(t *testing.T) {
	rl := newRateLimiter(2)
	defer rl.Stop()
	ctx := context.Background()

	// First two go through immediately.
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	if err := rl.Wait(ctx); err != nil {
		t.Fatalf("second Wait: %v", err)
	}

	// Third should block briefly until the refiller restocks.
	deadline := time.Now().Add(2 * time.Second)
	ctxTimeout, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := rl.Wait(ctxTimeout); err != nil {
		t.Errorf("third Wait should eventually succeed within budget: %v", err)
	}
}

func TestNoopSender_AlwaysErrors(t *testing.T) {
	s := NewNoopSender()
	_, err := s.Send(context.Background(), Message{To: []string{testEmailAddr}})
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("noop should return ErrNotConfigured: %v", err)
	}
}
