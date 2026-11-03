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
	m, err := BuildEmailOTPEmail(EmailOTPData{UserEmail: "u@x.com", Code: "04291735"})
	if err != nil {
		t.Fatalf("BuildEmailOTPEmail: %v", err)
	}
	if m.Tags["template"] != "email_otp" || strings.Contains(m.Subject, "04291735") {
		t.Errorf("message = %+v; the subject must not carry the code", m)
	}
	for name, body := range map[string]string{"html": m.HTMLBody, "text": m.TextBody} {
		if !strings.Contains(body, "04291735") || !strings.Contains(body, "10 minutes") {
			t.Errorf("%s body lacks the code or its default lifetime: %s", name, body)
		}
	}
	for _, code := range []string{"", "042917", "1234567", "123456789", "1234567a", "<b>1</b>"} {
		if _, err := BuildEmailOTPEmail(EmailOTPData{UserEmail: "u@x.com", Code: code}); err == nil {
			t.Errorf("code %q: built", code)
		}
	}
}

func TestSignInEmailsNameTheProjectAndItsSite(t *testing.T) {
	link, err := BuildMagicLinkEmail(MagicLinkData{UserEmail: "u@x.com", LinkURL: "https://notes.acme.test/cb?token_hash=abc",
		ProductName: "Acme <Notes>", SiteURL: "https://notes.acme.test/app"})
	if err != nil {
		t.Fatal(err)
	}
	code, err := BuildEmailOTPEmail(EmailOTPData{UserEmail: "u@x.com", Code: "04291735",
		ProductName: "Acme <Notes>", SiteURL: "https://notes.acme.test/app"})
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]Message{"link": link, "code": code} {
		if !strings.Contains(m.Subject, "Acme <Notes>") {
			t.Errorf("%s subject names no project: %q", name, m.Subject)
		}
		if !strings.Contains(m.TextBody, "Acme <Notes>") || !strings.Contains(m.TextBody, "notes.acme.test") {
			t.Errorf("%s text names no project or site: %s", name, m.TextBody)
		}
		if m.FromName != "Acme Notes via Excalibase" {
			t.Errorf("%s sender = %q, want the project via Excalibase", name, m.FromName)
		}
		for part, body := range map[string]string{"html": m.HTMLBody, "text": m.TextBody} {
			if !strings.Contains(body, "Sent by Excalibase on behalf of") {
				t.Errorf("%s %s footer does not say who sent it", name, part)
			}
		}
		if !strings.Contains(m.HTMLBody, "Acme &lt;Notes&gt;") || strings.Contains(m.HTMLBody, "<Notes>") || !strings.Contains(m.HTMLBody, "notes.acme.test") {
			t.Errorf("%s html names no project or site, or leaves markup unescaped", name)
		}
	}
}

func TestSignInSenderNameIsThePlatformWhenTheProjectHasNone(t *testing.T) {
	m, err := BuildEmailOTPEmail(EmailOTPData{UserEmail: "u@x.com", Code: "04291735"})
	if err != nil || m.FromName != "Excalibase" {
		t.Fatalf("sender = %q %v", m.FromName, err)
	}
}

func TestAddressLinkEmailsSayTheyAddTheAddressToAnAccount(t *testing.T) {
	link, err := BuildLinkMagicLinkEmail(MagicLinkData{UserEmail: "u@x.com", LinkURL: "https://app.test/cb?token_hash=abc", ProductName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	code, err := BuildLinkEmailOTPEmail(EmailOTPData{UserEmail: "u@x.com", Code: "04291735", ProductName: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]Message{"link_magic_link": link, "link_email_otp": code} {
		if m.Tags["template"] != name || strings.Contains(m.Subject, "04291735") || !strings.Contains(m.Subject, "Acme") {
			t.Errorf("%s: message = %+v", name, m)
		}
		for part, body := range map[string]string{"html": m.HTMLBody, "text": m.TextBody} {
			if !strings.Contains(body, "add u@x.com to an account") || !strings.Contains(body, "Never share") ||
				strings.Contains(body, "to sign in") {
				t.Errorf("%s %s body does not say it links the address: %s", name, part, body)
			}
		}
	}
	if !strings.Contains(link.TextBody, "token_hash=abc") || !strings.Contains(code.TextBody, "04291735") {
		t.Error("the secret is not in the mail")
	}
	if _, err := BuildLinkMagicLinkEmail(MagicLinkData{LinkURL: "javascript:alert(1)"}); err == nil {
		t.Error("a script link built")
	}
	if _, err := BuildLinkEmailOTPEmail(EmailOTPData{Code: "123"}); err == nil {
		t.Error("a short code built")
	}
}
