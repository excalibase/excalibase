package email

import (
	"strings"
	"testing"
)

const accountProject = "Acme Notes"

func bodies(m Message) map[string]string {
	return map[string]string{"html": m.HTMLBody, "text": m.TextBody}
}

func TestBuildEmailChangeEmail(t *testing.T) {
	link := "https://notes.acme.test/cb?token_hash=abc&type=email_change"
	m, err := BuildEmailChangeEmail(MagicLinkData{UserEmail: "new@x.com", LinkURL: link, ExpiresMin: 30,
		ProductName: accountProject, SiteURL: "https://notes.acme.test"})
	if err != nil {
		t.Fatalf("BuildEmailChangeEmail: %v", err)
	}
	if m.Subject != "Confirm your new email address for Acme Notes" || m.Tags["template"] != "email_change" ||
		m.FromName != "Acme Notes via Excalibase" {
		t.Errorf("message = %+v", m)
	}
	for name, body := range bodies(m) {
		for _, want := range []string{"new@x.com", "token_hash=abc", "30 minutes", "works once", "notes.acme.test", "sign-in address"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body lacks %q: %s", name, want, body)
			}
		}
		if strings.Contains(body, "Your sign-in to") {
			t.Errorf("%s body reads as a sign-in: %s", name, body)
		}
	}
	if !strings.Contains(m.TextBody, link) {
		t.Errorf("the plain-text link is not the link as sent: %s", m.TextBody)
	}
	for name, data := range map[string]MagicLinkData{
		"no link":       {UserEmail: "new@x.com"},
		"relative link": {UserEmail: "new@x.com", LinkURL: "/cb?token_hash=abc"},
		"script link":   {UserEmail: "new@x.com", LinkURL: "javascript:alert(1)"},
	} {
		if _, err := BuildEmailChangeEmail(data); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}

func TestBuildEmailChangeNoticeEmail(t *testing.T) {
	m, err := BuildEmailChangeNoticeEmail(EmailChangeNoticeData{UserEmail: "old@x.com", NewEmail: "new@x.com", ProductName: accountProject})
	if err != nil {
		t.Fatalf("BuildEmailChangeNoticeEmail: %v", err)
	}
	if m.Subject != "Your email address for Acme Notes is being changed" || m.Tags["template"] != "email_change_notice" {
		t.Errorf("message = %+v", m)
	}
	for name, body := range bodies(m) {
		for _, want := range []string{"new@x.com", "Nothing changes until", "If this wasn't you, sign in and change your password"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body lacks %q: %s", name, want, body)
			}
		}
	}
	for _, bad := range []string{"", "not-an-address", "a@x.com\nBcc: b@x.com", "Name <a@x.com>"} {
		if _, err := BuildEmailChangeNoticeEmail(EmailChangeNoticeData{UserEmail: "old@x.com", NewEmail: bad}); err == nil {
			t.Errorf("new address %q: built", bad)
		}
	}
}

func TestBuildIdentityLinkedEmail(t *testing.T) {
	for method, named := range map[string]string{"google": "Google", "github": "GitHub", "email": "email address extra@x.com"} {
		m, err := BuildIdentityLinkedEmail(IdentityLinkedData{UserEmail: "u@x.com", Method: method, LinkedEmail: "extra@x.com", ProductName: accountProject})
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if m.Subject != "A new sign-in method was added to your Acme Notes account" || m.Tags["template"] != "identity_linked" {
			t.Errorf("%s: message = %+v", method, m)
		}
		for name, body := range bodies(m) {
			for _, want := range []string{named, "If this wasn't you, sign in, remove it from your account and change your password."} {
				if !strings.Contains(body, want) {
					t.Errorf("%s %s body lacks %q: %s", method, name, want, body)
				}
			}
		}
	}
	if m, err := BuildIdentityLinkedEmail(IdentityLinkedData{UserEmail: "u@x.com", Method: "github"}); err != nil || strings.Contains(m.TextBody, "()") {
		t.Errorf("no linked address: %v %s", err, m.TextBody)
	}
	for name, data := range map[string]IdentityLinkedData{
		"unknown method":   {UserEmail: "u@x.com", Method: "facebook"},
		"no method":        {UserEmail: "u@x.com"},
		"bad linked email": {UserEmail: "u@x.com", Method: "email", LinkedEmail: "x\r\ny"},
	} {
		if _, err := BuildIdentityLinkedEmail(data); err == nil {
			t.Errorf("%s: built", name)
		}
	}
}
