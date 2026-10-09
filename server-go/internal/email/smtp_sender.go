package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
)

// SMTP transport security. STARTTLS is the default: a server that does not
// offer it is refused rather than silently spoken to in plain text.
const (
	SMTPTLSStartTLS = "starttls"
	SMTPTLSImplicit = "tls"
	SMTPTLSNone     = "none"
)

const defaultSMTPTimeout = 30 * time.Second

// SMTPConfig is the plain-SMTP provider for self-hosted installs. Password
// comes from a Secret; it is never logged or echoed in errors.
type SMTPConfig struct {
	Host     string
	Port     int
	TLSMode  string // starttls (default), tls or none
	Username string
	Password string
	// CAFile trusts a private CA besides the system roots, for a relay whose
	// certificate an internal CA issued.
	CAFile          string
	DefaultFrom     string
	DefaultFromName string
	SendsPerSecond  int
	Timeout         time.Duration
}

// SMTPSender sends transactional email through an SMTP relay.
type SMTPSender struct {
	cfg       SMTPConfig
	tlsConfig *tls.Config
	rl        *rateLimiter
}

// NewSMTPSender validates cfg; a misconfiguration fails here, at boot, not on
// the first sign-up.
func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if cfg.TLSMode == "" {
		cfg.TLSMode = SMTPTLSStartTLS
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultSMTPTimeout
	}
	if cfg.Port == 0 {
		cfg.Port = defaultSMTPPort(cfg.TLSMode)
	}
	if err := validateSMTPConfig(cfg); err != nil {
		return nil, err
	}
	tlsConfig, err := smtpTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	rate := cfg.SendsPerSecond
	if rate <= 0 {
		rate = 5
	}
	return &SMTPSender{cfg: cfg, tlsConfig: tlsConfig, rl: newRateLimiter(rate)}, nil
}

func defaultSMTPPort(mode string) int {
	switch mode {
	case SMTPTLSImplicit:
		return 465
	case SMTPTLSNone:
		return 25
	}
	return 587
}

func validateSMTPConfig(cfg SMTPConfig) error {
	switch {
	case cfg.Host == "":
		return errors.New("smtp: host required")
	case cfg.Port <= 0 || cfg.Port > 65535:
		return fmt.Errorf("smtp: port %d out of range", cfg.Port)
	case cfg.DefaultFrom == "":
		return errors.New("smtp: default From address required")
	case cfg.TLSMode != SMTPTLSStartTLS && cfg.TLSMode != SMTPTLSImplicit && cfg.TLSMode != SMTPTLSNone:
		return fmt.Errorf("smtp: tls mode %q is not starttls, tls or none", cfg.TLSMode)
	case cfg.Username != "" && cfg.Password == "":
		return errors.New("smtp: a username needs a password")
	case cfg.TLSMode == SMTPTLSNone && cfg.Username != "":
		return errors.New("smtp: credentials are never sent without TLS; use starttls or tls")
	}
	if _, err := mail.ParseAddress(cfg.DefaultFrom); err != nil {
		return fmt.Errorf("smtp: default From %q: %w", cfg.DefaultFrom, err)
	}
	return nil
}

func smtpTLSConfig(cfg SMTPConfig) (*tls.Config, error) {
	tlsConfig := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if cfg.CAFile == "" {
		return tlsConfig, nil
	}
	pemBytes, err := os.ReadFile(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("smtp: read CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("smtp: CA file %s holds no PEM certificate", cfg.CAFile)
	}
	tlsConfig.RootCAs = pool
	return tlsConfig, nil
}

// Stop releases the rate limiter goroutine.
func (s *SMTPSender) Stop() { s.rl.Stop() }

// Send delivers msg and returns the Message-Id it stamped.
func (s *SMTPSender) Send(ctx context.Context, msg Message) (string, error) {
	recipients, err := parseRecipients(msg.To)
	if err != nil {
		return "", err
	}
	from := s.fromAddress(msg)
	messageID, body, err := buildMIME(from, recipients, msg)
	if err != nil {
		return "", err
	}
	if err := s.rl.Wait(ctx); err != nil {
		return "", err
	}
	if err := s.deliver(ctx, from.Address, recipients, body); err != nil {
		return "", err
	}
	return messageID, nil
}

func (s *SMTPSender) fromAddress(msg Message) mail.Address {
	address := msg.From
	if address == "" {
		address = s.cfg.DefaultFrom
	}
	name := msg.FromName
	if name == "" {
		name = s.cfg.DefaultFromName
	}
	return mail.Address{Name: name, Address: address}
}

func parseRecipients(to []string) ([]*mail.Address, error) {
	if err := validateRecipients(to); err != nil {
		return nil, err
	}
	parsed := make([]*mail.Address, 0, len(to))
	for _, raw := range to {
		if strings.ContainsAny(raw, "\r\n") {
			return nil, fmt.Errorf("%w: line break in address", ErrInvalidRecipient)
		}
		address, err := mail.ParseAddress(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrInvalidRecipient, raw)
		}
		parsed = append(parsed, address)
	}
	return parsed, nil
}

func (s *SMTPSender) deliver(ctx context.Context, from string, recipients []*mail.Address, body []byte) error {
	client, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp: authenticate as %s: %w", s.cfg.Username, classifySMTPError(err))
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", classifySMTPError(err))
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient.Address); err != nil {
			return fmt.Errorf("smtp: RCPT TO: %w", classifySMTPError(err))
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", classifySMTPError(err))
	}
	if _, err := writer.Write(body); err != nil {
		return fmt.Errorf("smtp: write message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("smtp: message refused: %w", classifySMTPError(err))
	}
	return client.Quit()
}

// connect dials, greets and secures the session before any credential or
// address crosses the wire.
func (s *SMTPSender) connect(ctx context.Context) (*smtp.Client, error) {
	address := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{Timeout: s.cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("smtp: dial %s: %w", address, err)
	}
	_ = conn.SetDeadline(time.Now().Add(s.cfg.Timeout))
	if s.cfg.TLSMode == SMTPTLSImplicit {
		tlsConn := tls.Client(conn, s.tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("smtp: TLS handshake with %s: %w", address, err)
		}
		conn = tlsConn
	}
	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("smtp: greet %s: %w", address, err)
	}
	if err := client.Hello(localHostname()); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("smtp: EHLO %s: %w", address, err)
	}
	if s.cfg.TLSMode != SMTPTLSStartTLS {
		return client, nil
	}
	if ok, _ := client.Extension("STARTTLS"); !ok {
		_ = client.Close()
		return nil, fmt.Errorf("smtp: %s does not offer STARTTLS; set the tls mode to tls for port 465", address)
	}
	if err := client.StartTLS(s.tlsConfig); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("smtp: STARTTLS with %s: %w", address, err)
	}
	return client, nil
}

func localHostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" || strings.ContainsAny(name, " \r\n") {
		return "localhost"
	}
	return name
}

// classifySMTPError maps reply codes onto the package's typed errors.
func classifySMTPError(err error) error {
	var protoErr *textproto.Error
	if !errors.As(err, &protoErr) {
		return err
	}
	switch protoErr.Code {
	case 550, 551, 553, 501:
		return fmt.Errorf("%w: %d %s", ErrInvalidRecipient, protoErr.Code, protoErr.Msg)
	case 421, 450, 451, 452:
		return fmt.Errorf("%w: %d %s", ErrRateLimited, protoErr.Code, protoErr.Msg)
	}
	return err
}

// buildMIME renders a multipart/alternative message with quoted-printable
// parts. Header values are encoded, so no caller string can add a header line.
func buildMIME(from mail.Address, to []*mail.Address, msg Message) (string, []byte, error) {
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return "", nil, errors.New("smtp: line break in subject")
	}
	messageID, err := newMessageID(from.Address)
	if err != nil {
		return "", nil, err
	}
	recipients := make([]string, 0, len(to))
	for _, address := range to {
		recipients = append(recipients, address.String())
	}
	var buf bytes.Buffer
	parts := multipart.NewWriter(&buf)
	headers := []string{
		"From: " + from.String(),
		"To: " + strings.Join(recipients, ", "),
		"Subject: " + mime.QEncoding.Encode("utf-8", msg.Subject),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"Message-Id: " + messageID,
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=" + parts.Boundary(),
	}
	if msg.ReplyTo != "" {
		replyTo, err := mail.ParseAddress(msg.ReplyTo)
		if err != nil {
			return "", nil, fmt.Errorf("smtp: reply-to %q: %w", msg.ReplyTo, err)
		}
		headers = append(headers, "Reply-To: "+replyTo.String())
	}
	var out bytes.Buffer
	out.WriteString(strings.Join(headers, "\r\n") + "\r\n\r\n")
	if err := writeParts(parts, msg); err != nil {
		return "", nil, err
	}
	out.Write(buf.Bytes())
	return messageID, out.Bytes(), nil
}

func writeParts(parts *multipart.Writer, msg Message) error {
	bodies := []struct{ contentType, body string }{
		{"text/plain; charset=utf-8", msg.TextBody},
		{"text/html; charset=utf-8", msg.HTMLBody},
	}
	for _, part := range bodies {
		if part.body == "" {
			continue
		}
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", part.contentType)
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		writer, err := parts.CreatePart(header)
		if err != nil {
			return fmt.Errorf("smtp: build message: %w", err)
		}
		encoder := quotedprintable.NewWriter(writer)
		if _, err := encoder.Write([]byte(part.body)); err != nil {
			return fmt.Errorf("smtp: build message: %w", err)
		}
		if err := encoder.Close(); err != nil {
			return fmt.Errorf("smtp: build message: %w", err)
		}
	}
	return parts.Close()
}

func newMessageID(from string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("smtp: message id: %w", err)
	}
	domain := "localhost"
	if at := strings.LastIndex(from, "@"); at >= 0 && at < len(from)-1 {
		domain = from[at+1:]
	}
	return "<" + hex.EncodeToString(random) + "@" + domain + ">", nil
}
