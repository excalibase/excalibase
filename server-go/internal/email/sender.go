// Package email provides transactional email sending. The Sender interface
// is intentionally minimal so we can swap providers (SES → SMTP → SendGrid)
// by changing one constructor call. Implementations live alongside in this
// package; pick one in main.go based on EMAIL_PROVIDER env.
package email

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// Message is provider-agnostic. Tags map to SES configuration sets when the
// provider supports them (used for bounce/complaint tracking and per-template
// suppression lists). Other providers MAY ignore Tags.
type Message struct {
	To       []string
	From     string
	FromName string
	Subject  string
	HTMLBody string
	TextBody string // optional fallback for clients that don't render HTML
	ReplyTo  string
	Tags     map[string]string
}

// Sender abstracts the underlying provider. Concrete impls: SESSender,
// noopSender (tests). Implementations MUST be safe for concurrent calls.
type Sender interface {
	// Send dispatches the message and returns the provider-issued message ID
	// (used to correlate bounces/complaints). Errors are wrapped so callers
	// can errors.Is them against ErrInvalidRecipient / ErrRateLimited.
	Send(ctx context.Context, msg Message) (messageID string, err error)
}

// Errors callers can branch on. Providers convert their native errors to
// these so handler code doesn't depend on the concrete provider.
var (
	ErrInvalidRecipient = errors.New("email: invalid recipient")
	ErrRateLimited      = errors.New("email: provider rate-limited")
	ErrSandbox          = errors.New("email: provider in sandbox, recipient not verified")
	ErrNotConfigured    = errors.New("email: sender not configured")
)

// noopSender is the zero-value Sender used when EMAIL_PROVIDER is unset.
// Returns ErrNotConfigured so handlers can short-circuit cleanly without
// panicking on nil dereference.
type noopSender struct{}

// NewNoopSender returns a Sender that always errors. Use it in tests or
// when SMTP isn't configured yet — handlers stay code-complete.
func NewNoopSender() Sender { return noopSender{} }

func (noopSender) Send(ctx context.Context, _ Message) (string, error) {
	return "", ErrNotConfigured
}

// Render is a small helper used by template-based call sites. Each template
// has its own filename in templates/ but the rendering logic is the same:
// HTML body + plain-text fallback derived from the same data.
func Render(htmlTemplate, textTemplate string, data interface{}) (htmlBody, textBody string, err error) {
	if htmlTemplate != "" {
		t, err := template.New("html").Parse(htmlTemplate)
		if err != nil {
			return "", "", fmt.Errorf("parse html: %w", err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return "", "", fmt.Errorf("exec html: %w", err)
		}
		htmlBody = buf.String()
	}
	if textTemplate != "" {
		t, err := template.New("text").Parse(textTemplate)
		if err != nil {
			return "", "", fmt.Errorf("parse text: %w", err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return "", "", fmt.Errorf("exec text: %w", err)
		}
		textBody = buf.String()
	}
	return htmlBody, textBody, nil
}

// rateLimiter is a simple token bucket used by SESSender to keep us under
// the per-second send rate (default 14/sec for fresh SES accounts). Reused
// here rather than pulling a new dep.
type rateLimiter struct {
	tokens chan struct{}
	stop   chan struct{}
}

// newRateLimiter starts a goroutine that refills the bucket at perSecond
// rate. Call Wait before each Send. Stop() halts the refiller.
func newRateLimiter(perSecond int) *rateLimiter {
	if perSecond <= 0 {
		perSecond = 14
	}
	rl := &rateLimiter{
		tokens: make(chan struct{}, perSecond),
		stop:   make(chan struct{}),
	}
	for i := 0; i < perSecond; i++ {
		rl.tokens <- struct{}{}
	}
	go func() {
		t := time.NewTicker(time.Second / time.Duration(perSecond))
		defer t.Stop()
		for {
			select {
			case <-rl.stop:
				return
			case <-t.C:
				select {
				case rl.tokens <- struct{}{}:
				default: // bucket full
				}
			}
		}
	}()
	return rl
}

func (rl *rateLimiter) Wait(ctx context.Context) error {
	select {
	case <-rl.tokens:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (rl *rateLimiter) Stop() { close(rl.stop) }

// validateRecipients catches obvious typos before we hit SES. SES will
// reject malformed addresses anyway, but failing locally saves a request.
func validateRecipients(to []string) error {
	if len(to) == 0 {
		return fmt.Errorf("%w: empty recipient list", ErrInvalidRecipient)
	}
	for _, addr := range to {
		if !strings.Contains(addr, "@") || strings.HasPrefix(addr, "@") || strings.HasSuffix(addr, "@") {
			return fmt.Errorf("%w: %q", ErrInvalidRecipient, addr)
		}
	}
	return nil
}
