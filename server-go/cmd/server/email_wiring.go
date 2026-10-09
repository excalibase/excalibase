package main

import (
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/email"
)

// buildSMTPSender builds the self-hosted SMTP relay sender (EXC-580). Unlike
// the cloud providers it errors instead of degrading to a no-op: an operator
// who chose SMTP wants a boot failure, not 503s at the first sign-up.
func buildSMTPSender(cfg config.AppConfig) (email.Sender, error) {
	from := envOr("SMTP_FROM_ADDRESS", envOr("EMAIL_FROM_ADDRESS", cfg.EmailFromAddress))
	fromName := envOr("SMTP_FROM_NAME", envOr("EMAIL_FROM_NAME", cfg.EmailFromName))
	sender, err := email.NewSMTPSender(email.SMTPConfig{
		Host:            cfg.SMTPHost,
		Port:            cfg.SMTPPort,
		TLSMode:         cfg.SMTPTLS,
		Username:        cfg.SMTPUsername,
		Password:        cfg.SMTPPassword,
		CAFile:          cfg.SMTPCAFile,
		DefaultFrom:     from,
		DefaultFromName: fromName,
	})
	if err != nil {
		return nil, fmt.Errorf("EMAIL_PROVIDER=smtp: %w", err)
	}
	tlsMode := cfg.SMTPTLS
	if tlsMode == "" {
		tlsMode = email.SMTPTLSStartTLS
	}
	if tlsMode == email.SMTPTLSNone {
		log.Printf("WARN: SMTP relay %s is used without TLS; keep it on a private network", cfg.SMTPHost)
	}
	log.Printf("INFO: SMTP sender configured (host=%s tls=%s auth=%t from=%s)",
		cfg.SMTPHost, tlsMode, cfg.SMTPUsername != "", from)
	return sender, nil
}
