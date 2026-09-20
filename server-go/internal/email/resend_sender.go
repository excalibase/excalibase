package email

import (
	"context"
	"errors"
	"fmt"
	"strings"

	resend "github.com/resend/resend-go/v3"
)

// resendEmailsAPI is the subset of resend.EmailsSvc the adapter
// uses. Defining it here lets tests fake the SDK without spinning
// up an httptest.Server, and keeps the SDK surface we depend on
// explicit (so future SDK breaking changes are loud, not silent).
type resendEmailsAPI interface {
	SendWithContext(ctx context.Context, params *resend.SendEmailRequest) (*resend.SendEmailResponse, error)
}

// ResendSender sends transactional email via Resend (resend.com).
// Same Sender contract as SESSender — handlers don't change when
// the platform swaps providers via EMAIL_PROVIDER env.
type ResendSender struct {
	emails          resendEmailsAPI
	defaultFrom     string
	defaultFromName string
	rl              *rateLimiter
}

// ResendConfig holds the runtime config. APIKey lives in vault
// (path: email/resend) or in the RESEND_API_KEY env. SendsPerSecond
// matches Resend's free-tier limit (2/s) by default; production
// tier raises this — main.go sets the actual value from config.
type ResendConfig struct {
	APIKey          string
	DefaultFrom     string
	DefaultFromName string
	SendsPerSecond  int
}

// NewResendSender constructs a sender backed by the official SDK.
func NewResendSender(cfg ResendConfig) (*ResendSender, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("resend: APIKey required")
	}
	if cfg.DefaultFrom == "" {
		return nil, fmt.Errorf("resend: default From address required")
	}
	client := resend.NewClient(cfg.APIKey)
	rate := cfg.SendsPerSecond
	if rate <= 0 {
		rate = 2 // Resend free tier
	}
	return &ResendSender{
		emails:          client.Emails,
		defaultFrom:     cfg.DefaultFrom,
		defaultFromName: cfg.DefaultFromName,
		rl:              newRateLimiter(rate),
	}, nil
}

// newResendSenderForTest is the tests-only constructor that lets
// us inject a fake EmailsSvc. Not exported.
func newResendSenderForTest(api resendEmailsAPI, from, fromName string, ratePerSec int) *ResendSender {
	return &ResendSender{
		emails:          api,
		defaultFrom:     from,
		defaultFromName: fromName,
		rl:              newRateLimiter(ratePerSec),
	}
}

// Stop releases the rate limiter goroutine. Call from main.go's
// shutdown hook. Symmetric with SESSender.Stop.
func (s *ResendSender) Stop() { s.rl.Stop() }

// Send dispatches the message via Resend's /emails endpoint and
// returns the Resend message id (re_...). Errors are wrapped into
// the package's typed errors so handler code stays provider-agnostic.
func (s *ResendSender) Send(ctx context.Context, msg Message) (string, error) {
	if err := validateRecipients(msg.To); err != nil {
		return "", err
	}
	if err := s.rl.Wait(ctx); err != nil {
		return "", err
	}

	from := msg.From
	if from == "" {
		from = s.defaultFrom
	}
	fromName := msg.FromName
	if fromName == "" {
		fromName = s.defaultFromName
	}
	source := from
	if fromName != "" {
		source = fmt.Sprintf("%s <%s>", fromName, from)
	}

	params := &resend.SendEmailRequest{
		From:    source,
		To:      msg.To,
		Subject: msg.Subject,
		Html:    msg.HTMLBody,
		Text:    msg.TextBody,
	}
	if msg.ReplyTo != "" {
		params.ReplyTo = msg.ReplyTo
	}
	if len(msg.Tags) > 0 {
		tags := make([]resend.Tag, 0, len(msg.Tags))
		for k, v := range msg.Tags {
			tags = append(tags, resend.Tag{Name: k, Value: v})
		}
		params.Tags = tags
	}

	resp, err := s.emails.SendWithContext(ctx, params)
	if err != nil {
		return "", classifyResendError(err)
	}
	return resp.Id, nil
}

// classifyResendError maps the SDK's error shapes onto the package's
// typed errors. Resend's SDK only types rate-limit errors (sentinel
// `ErrRateLimit` + typed `*RateLimitError`); 4xx other than 429
// arrives as a plain `errors.New("[ERROR]: <api message>")`. We
// substring-match those on phrases the Resend API actually returns
// — fragile but unavoidable until the SDK adopts typed errors for
// validation responses (the SDK has a pending note marking this).
func classifyResendError(err error) error {
	if errors.Is(err, resend.ErrRateLimit) {
		return fmt.Errorf("%w: %v", ErrRateLimited, err)
	}
	msg := err.Error()
	low := strings.ToLower(msg)

	// Resend's verified-sender rejection is the equivalent of SES sandbox.
	// The API message reads "The 'from' address (...) is not verified" or
	// "domain not verified" — either form falls under ErrSandbox so callers
	// can branch the same way they do with SES.
	if strings.Contains(low, "not verified") || strings.Contains(low, "domain not") {
		return fmt.Errorf("%w: %s", ErrSandbox, msg)
	}
	// 422 / 400 always carry "validation_error" in the API name field
	// or "is invalid" in the message. Both indicate a bad recipient or
	// payload from the caller's perspective.
	if strings.Contains(low, "validation") || strings.Contains(low, "invalid") {
		return fmt.Errorf("%w: %s", ErrInvalidRecipient, msg)
	}
	return err
}
