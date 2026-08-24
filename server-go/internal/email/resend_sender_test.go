package email

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	resend "github.com/resend/resend-go/v3"
)

// fakeResendEmails records calls + lets tests inject responses /
// errors. Implements just the EmailsSvc subset we use (Send, with
// context); the rest stays unused for our adapter.
type fakeResendEmails struct {
	mu       sync.Mutex
	captured *resend.SendEmailRequest
	resp     *resend.SendEmailResponse
	err      error
}

func (f *fakeResendEmails) SendWithContext(_ context.Context, params *resend.SendEmailRequest) (*resend.SendEmailResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captured = params
	if f.err != nil {
		return nil, f.err
	}
	if f.resp != nil {
		return f.resp, nil
	}
	return &resend.SendEmailResponse{Id: "re_default"}, nil
}

func TestResendSender_Send_Happy(t *testing.T) {
	fake := &fakeResendEmails{resp: &resend.SendEmailResponse{Id: "re_abc123"}}
	s := newResendSenderForTest(fake, "noreply@x.test", "Excalibase", 100)

	id, err := s.Send(context.Background(), Message{
		To: []string{"user@example.com"}, Subject: "hi", HTMLBody: "<p>hi</p>", TextBody: "hi",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "re_abc123" {
		t.Errorf("id: got %q, want re_abc123", id)
	}
	if fake.captured == nil {
		t.Fatal("Send was not called")
	}
	if got := fake.captured.From; !strings.Contains(got, "Excalibase") || !strings.Contains(got, "noreply@x.test") {
		t.Errorf("From: %q", got)
	}
	if len(fake.captured.To) != 1 || fake.captured.To[0] != "user@example.com" {
		t.Errorf("To: %v", fake.captured.To)
	}
	if fake.captured.Html != "<p>hi</p>" || fake.captured.Text != "hi" {
		t.Errorf("body fields: html=%q text=%q", fake.captured.Html, fake.captured.Text)
	}
}

func TestResendSender_Send_PerMessageFromOverridesDefault(t *testing.T) {
	fake := &fakeResendEmails{}
	s := newResendSenderForTest(fake, "default@x.test", "DefaultName", 100)

	if _, err := s.Send(context.Background(), Message{
		To: []string{"u@x.test"}, From: "ops@x.test", FromName: "Ops",
		Subject: "s", HTMLBody: "<p/>",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := fake.captured.From; !strings.Contains(got, "Ops") || !strings.Contains(got, "ops@x.test") {
		t.Errorf("From should override default: %q", got)
	}
}

func TestResendSender_Send_InvalidRecipientShortCircuit(t *testing.T) {
	fake := &fakeResendEmails{}
	s := newResendSenderForTest(fake, "noreply@x.test", "X", 100)
	_, err := s.Send(context.Background(), Message{To: []string{"not-an-email"}, Subject: "s", HTMLBody: "<p/>"})
	if !errors.Is(err, ErrInvalidRecipient) {
		t.Errorf("got %v, want ErrInvalidRecipient", err)
	}
	if fake.captured != nil {
		t.Errorf("Resend should not have been called for invalid recipient")
	}
}

func TestResendSender_Send_RateLimitTypedMappedToErrRateLimited(t *testing.T) {
	// resend.RateLimitError satisfies errors.Is(_, resend.ErrRateLimit)
	// via the SDK's Is() method — same path the production SDK
	// returns from on a 429.
	fake := &fakeResendEmails{err: &resend.RateLimitError{Message: "too many requests"}}
	s := newResendSenderForTest(fake, "noreply@x.test", "X", 100)
	_, err := s.Send(context.Background(), Message{To: []string{"u@x.test"}, Subject: "s", HTMLBody: "<p/>"})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("rate limit: got %v, want ErrRateLimited", err)
	}
}

func TestResendSender_Send_ErrorClassificationByMessage(t *testing.T) {
	// Resend's SDK collapses 422/400 into plain `errors.New("[ERROR]: ...")`
	// — see resend.go:251. We substring-match the API messages the
	// service actually returns. These literals come from the Resend
	// docs (https://resend.com/docs/api-reference/errors).
	cases := []struct {
		name string
		body string
		want error
	}{
		{"validation error → InvalidRecipient",
			"[ERROR]: validation_error: email is invalid", ErrInvalidRecipient},
		{"invalid argument → InvalidRecipient",
			"[ERROR]: subject is invalid", ErrInvalidRecipient},
		{"unverified sender → Sandbox",
			"[ERROR]: The 'from' address is not verified", ErrSandbox},
		{"domain not configured → Sandbox",
			"[ERROR]: domain not configured", ErrSandbox},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &fakeResendEmails{err: errors.New(c.body)}
			s := newResendSenderForTest(fake, "noreply@x.test", "X", 100)
			_, err := s.Send(context.Background(), Message{
				To: []string{"u@x.test"}, Subject: "s", HTMLBody: "<p/>",
			})
			if !errors.Is(err, c.want) {
				t.Errorf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestResendSender_Send_RateLimiterCancelsOnContextDone(t *testing.T) {
	// Deliberately starve the limiter by setting tokens to 0 then
	// cancelling the context — Send must return ctx.Err() rather
	// than waiting forever.
	fake := &fakeResendEmails{}
	s := newResendSenderForTest(fake, "noreply@x.test", "X", 1)
	// Drain the single token so the next Wait blocks.
	<-s.rl.tokens

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Send(ctx, Message{To: []string{"u@x.test"}, Subject: "s", HTMLBody: "<p/>"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestNewResendSender_RequiresAPIKeyAndFrom(t *testing.T) {
	if _, err := NewResendSender(ResendConfig{}); err == nil {
		t.Error("expected error for missing api key")
	}
	if _, err := NewResendSender(ResendConfig{APIKey: "re_xxx"}); err == nil {
		t.Error("expected error for missing default From")
	}
}
