package email

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	smithy "github.com/aws/smithy-go"
)

const errWrapFmt = "%w: %s"


// SESSender sends transactional email via Amazon SES. We use the SES v2 API
// (sesv2) because the v1 SendEmail is deprecated and v2 has cleaner content
// types, configuration set support, and clearer error codes.
type SESSender struct {
	client     *sesv2.Client
	defaultFrom string
	defaultFromName string
	configSet  string // optional SES configuration set for tracking
	rl         *rateLimiter
}

// SESConfig holds everything SESSender needs. AccessKeyID + SecretAccessKey
// come from the K8s secret ses-creds; Region is also in the secret. From*
// fields are deployment-level defaults — caller can still override per-Send
// by setting Message.From / Message.FromName.
type SESConfig struct {
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	DefaultFrom     string
	DefaultFromName string
	ConfigurationSet string
	// SendsPerSecond rate-limits us locally to stay under the SES quota.
	// Default 14 (fresh SES accounts; production tier raises this).
	SendsPerSecond int
}

// NewSESSender constructs a sender with explicit credentials. We don't use
// the SDK's default credential chain here because the platform reads creds
// from a vault-managed K8s secret — passing them explicitly avoids the
// chain accidentally falling back to instance-profile creds (which on
// minikube would 403 silently).
func NewSESSender(cfg SESConfig) (*SESSender, error) {
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("ses: access key + secret required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.DefaultFrom == "" {
		return nil, fmt.Errorf("ses: default From address required")
	}
	awsCfg := aws.Config{
		Region: cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, "",
		),
	}
	return &SESSender{
		client:          sesv2.NewFromConfig(awsCfg),
		defaultFrom:     cfg.DefaultFrom,
		defaultFromName: cfg.DefaultFromName,
		configSet:       cfg.ConfigurationSet,
		rl:              newRateLimiter(cfg.SendsPerSecond),
	}, nil
}

// Stop releases the rate limiter goroutine. Wire this into main.go's
// shutdown hook if the process needs clean termination.
func (s *SESSender) Stop() { s.rl.Stop() }

// Send dispatches the message via SES v2 SendEmail. Returns the SES
// MessageId on success. Wraps SES errors into the package's typed
// errors so handler code can branch on them without importing aws-sdk.
func (s *SESSender) Send(ctx context.Context, msg Message) (string, error) {
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
	// SES expects "Name <email>" or just email. The display name is
	// optional; SES will use the email as-is when name is empty.
	source := from
	if fromName != "" {
		source = fmt.Sprintf("%s <%s>", fromName, from)
	}

	content := &types.EmailContent{
		Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(msg.Subject), Charset: aws.String("UTF-8")},
			Body:    &types.Body{},
		},
	}
	if msg.HTMLBody != "" {
		content.Simple.Body.Html = &types.Content{Data: aws.String(msg.HTMLBody), Charset: aws.String("UTF-8")}
	}
	if msg.TextBody != "" {
		content.Simple.Body.Text = &types.Content{Data: aws.String(msg.TextBody), Charset: aws.String("UTF-8")}
	}

	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(source),
		Destination:      &types.Destination{ToAddresses: msg.To},
		Content:          content,
	}
	if msg.ReplyTo != "" {
		input.ReplyToAddresses = []string{msg.ReplyTo}
	}
	if s.configSet != "" {
		input.ConfigurationSetName = aws.String(s.configSet)
	}
	if len(msg.Tags) > 0 {
		input.EmailTags = make([]types.MessageTag, 0, len(msg.Tags))
		for k, v := range msg.Tags {
			input.EmailTags = append(input.EmailTags, types.MessageTag{
				Name:  aws.String(k),
				Value: aws.String(v),
			})
		}
	}

	out, err := s.client.SendEmail(ctx, input)
	if err != nil {
		return "", classifySESError(err)
	}
	if out.MessageId == nil {
		return "", fmt.Errorf("ses: missing message id in response")
	}
	return *out.MessageId, nil
}

// classifySESError maps SES error codes onto the package's typed errors.
// We only special-case the ones callers will actually branch on; the rest
// pass through as the wrapped smithy error so they're still loggable.
func classifySESError(err error) error {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.ErrorCode() {
	case "MessageRejected":
		// Common in sandbox: "Email address is not verified."
		if strings.Contains(apiErr.ErrorMessage(), "not verified") {
			return fmt.Errorf(errWrapFmt, ErrSandbox, apiErr.ErrorMessage())
		}
		return fmt.Errorf(errWrapFmt, ErrInvalidRecipient, apiErr.ErrorMessage())
	case "Throttling", "TooManyRequests", "SendingPausedException":
		return fmt.Errorf(errWrapFmt, ErrRateLimited, apiErr.ErrorMessage())
	}
	return err
}
