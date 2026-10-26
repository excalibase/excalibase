package email

import (
	"strings"
	"testing"
)

func TestBuildMagicLinkEmail(t *testing.T) {
	link := "https://app.example.com/cb?token_hash=abc&type=magiclink"
	m, err := BuildMagicLinkEmail(MagicLinkData{UserEmail: "u@x.com", LinkURL: link, ExpiresMin: 10})
	if err != nil {
		t.Fatalf("BuildMagicLinkEmail: %v", err)
	}
	if m.Subject == "" || m.Tags["template"] != "magic_link" {
		t.Errorf("message = %+v", m)
	}
	for name, body := range map[string]string{"html": m.HTMLBody, "text": m.TextBody} {
		if !strings.Contains(body, "token_hash=abc") || !strings.Contains(body, "10 minutes") {
			t.Errorf("%s body lacks the link or its lifetime: %s", name, body)
		}
	}
	if !strings.Contains(m.TextBody, link) {
		t.Errorf("the plain-text link is not the link as sent: %s", m.TextBody)
	}
	for name, data := range map[string]MagicLinkData{
		"no link":       {UserEmail: "u@x.com"},
		"relative link": {UserEmail: "u@x.com", LinkURL: "/cb?token_hash=abc"},
		"script link":   {UserEmail: "u@x.com", LinkURL: "javascript:alert(1)"},
	} {
		if _, err := BuildMagicLinkEmail(data); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

func TestBuildEmailOTPEmail(t *testing.T) {
	m, err := BuildEmailOTPEmail(EmailOTPData{UserEmail: "u@x.com", Code: "042917"})
	if err != nil {
		t.Fatalf("BuildEmailOTPEmail: %v", err)
	}
	if m.Tags["template"] != "email_otp" || strings.Contains(m.Subject, "042917") {
		t.Errorf("message = %+v; the subject must not carry the code", m)
	}
	for name, body := range map[string]string{"html": m.HTMLBody, "text": m.TextBody} {
		if !strings.Contains(body, "042917") || !strings.Contains(body, "10 minutes") {
			t.Errorf("%s body lacks the code or its default lifetime: %s", name, body)
		}
	}
	for _, code := range []string{"", "12345", "1234567", "12345a", "<b>1</b>"} {
		if _, err := BuildEmailOTPEmail(EmailOTPData{UserEmail: "u@x.com", Code: code}); err == nil {
			t.Errorf("code %q: built", code)
		}
	}
}
