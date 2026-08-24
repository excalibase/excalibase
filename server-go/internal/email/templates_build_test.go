package email

import (
	"strings"
	"testing"
)

func TestBuildVerifyEmail(t *testing.T) {
	m, err := BuildVerifyEmail(VerifyEmailData{UserEmail: "u@x.com", VerifyURL: "https://x/verify"})
	if err != nil {
		t.Fatalf("BuildVerifyEmail: %v", err)
	}
	if m.HTMLBody == "" || m.TextBody == "" || m.Subject == "" {
		t.Errorf("empty fields: %+v", m)
	}
	if !strings.Contains(m.HTMLBody, "https://x/verify") {
		t.Error("verify URL not rendered into HTML")
	}
}

func TestBuildPasswordResetEmail(t *testing.T) {
	m, err := BuildPasswordResetEmail(PasswordResetData{UserEmail: "u@x.com", ResetURL: "https://x/reset"})
	if err != nil {
		t.Fatalf("BuildPasswordResetEmail: %v", err)
	}
	if m.Subject == "" || !strings.Contains(m.HTMLBody, "https://x/reset") {
		t.Errorf("reset email not rendered: %+v", m)
	}
}

func TestBuildOrgInviteEmail_DefaultsAndSubject(t *testing.T) {
	m, err := BuildOrgInviteEmail(OrgInviteData{OrgName: "Acme", AcceptURL: "https://x/accept"})
	if err != nil {
		t.Fatalf("BuildOrgInviteEmail: %v", err)
	}
	if !strings.Contains(m.Subject, "Acme") {
		t.Errorf("subject should mention org: %q", m.Subject)
	}
	if m.Tags["template"] != "org_invite" {
		t.Errorf("tag template = %q", m.Tags["template"])
	}
}

func TestBuildAlertEmail(t *testing.T) {
	m, err := BuildAlertEmail(AlertData{Title: "Disk full", Severity: "critical", Body: "node down"})
	if err != nil {
		t.Fatalf("BuildAlertEmail: %v", err)
	}
	if m.HTMLBody == "" || m.Subject == "" {
		t.Errorf("empty alert email: %+v", m)
	}
}
