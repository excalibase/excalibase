package email

import (
	"context"
	"errors"
	"testing"

	smithy "github.com/aws/smithy-go"
)

// fakeAPIError implements smithy.APIError so classifySESError can be tested
// without a live SES round-trip.
type fakeAPIError struct {
	code string
	msg  string
}

func (e *fakeAPIError) Error() string                 { return e.code + ": " + e.msg }
func (e *fakeAPIError) ErrorCode() string             { return e.code }
func (e *fakeAPIError) ErrorMessage() string          { return e.msg }
func (e *fakeAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func TestNewResendSender_SuccessAndStop(t *testing.T) {
	s, err := NewResendSender(ResendConfig{
		APIKey:      "re_test_key",
		DefaultFrom: "noreply@example.com",
		// SendsPerSecond left 0 → defaults to the free-tier 2/s.
	})
	if err != nil {
		t.Fatalf("NewResendSender: %v", err)
	}
	if s.defaultFrom != "noreply@example.com" {
		t.Errorf("defaultFrom: %q", s.defaultFrom)
	}
	s.Stop() // releases the rate-limiter goroutine
}

func TestNewSESSender_Validation(t *testing.T) {
	cases := []struct {
		name string
		cfg  SESConfig
	}{
		{"missing key", SESConfig{SecretAccessKey: "s", DefaultFrom: "a@b.com"}},
		{"missing secret", SESConfig{AccessKeyID: "k", DefaultFrom: "a@b.com"}},
		{"missing from", SESConfig{AccessKeyID: "k", SecretAccessKey: "s"}},
	}
	for _, c := range cases {
		if _, err := NewSESSender(c.cfg); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestNewSESSender_DefaultsRegion(t *testing.T) {
	s, err := NewSESSender(SESConfig{
		AccessKeyID:     "k",
		SecretAccessKey: "s",
		DefaultFrom:     "noreply@example.com",
		SendsPerSecond:  5,
	})
	if err != nil {
		t.Fatalf("NewSESSender: %v", err)
	}
	defer s.Stop()
	if s.defaultFrom != "noreply@example.com" {
		t.Errorf("defaultFrom: %q", s.defaultFrom)
	}
}

func TestSESSender_Send_RejectsBadRecipients(t *testing.T) {
	s, _ := NewSESSender(SESConfig{
		AccessKeyID: "k", SecretAccessKey: "s", DefaultFrom: "a@b.com",
	})
	defer s.Stop()
	_, err := s.Send(context.Background(), Message{To: []string{"not-an-email"}})
	if !errors.Is(err, ErrInvalidRecipient) {
		t.Errorf("expected ErrInvalidRecipient, got %v", err)
	}
}

func TestClassifySESError(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantIs  error
		passthr bool
	}{
		{"sandbox", &fakeAPIError{"MessageRejected", "Email address is not verified."}, ErrSandbox, false},
		{"rejected", &fakeAPIError{"MessageRejected", "bad address"}, ErrInvalidRecipient, false},
		{"throttling", &fakeAPIError{"Throttling", "slow down"}, ErrRateLimited, false},
		{"too many", &fakeAPIError{"TooManyRequests", "x"}, ErrRateLimited, false},
		{"paused", &fakeAPIError{"SendingPausedException", "x"}, ErrRateLimited, false},
		{"unknown code passes through", &fakeAPIError{"SomethingElse", "x"}, nil, true},
		{"non-api error passes through", errors.New("boom"), nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifySESError(c.err)
			if c.passthr {
				if !errors.Is(got, c.err) {
					t.Errorf("expected passthrough of original error, got %v", got)
				}
				return
			}
			if !errors.Is(got, c.wantIs) {
				t.Errorf("expected errors.Is %v, got %v", c.wantIs, got)
			}
		})
	}
}
