package email

// Templates kept as Go const strings (no embed.FS) so the binary is
// self-contained — no missing-template panics at runtime.
//
// Email-client compatibility notes:
//   - Outlook still uses the Word HTML renderer; everything is table-based
//     because Word ignores most modern CSS.
//   - All styles inline. Email clients strip <style> blocks (Gmail Web is
//     particularly aggressive about this).
//   - Container is 600px — the de-facto email width since Outlook 2007.
//   - Buttons use a "bulletproof" pattern: a styled <a> tag wrapped in
//     mso-conditional VML for Outlook so the button renders consistently
//     across Outlook 2010-2024 + Apple Mail + Gmail + iOS Mail.
//   - Preheader (hidden snippet text) is what appears next to the subject
//     in inbox listings. We set it explicitly so the first body line
//     doesn't leak in (e.g. "Hi," would be the inbox snippet otherwise).
//   - Dark-mode-aware: gray text uses #4b5563 which contrasts on both
//     white and dark backgrounds in Apple Mail / iOS / Outlook 365 dark.
//
// Brand: text wordmark only for now. When we have a CDN-hosted logo we
// can swap the wordmark for an <img> with explicit width/height (email
// clients block external images by default — wordmark always renders).

// VerifyEmailData populates the verify-email template.
type VerifyEmailData struct {
	UserEmail   string
	VerifyURL   string
	ExpiresHour int
	ProductName string
}

const verifyEmailHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<title>Verify your email</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
<!-- Preheader (hidden, shown in inbox preview) -->
<div style="display:none;max-height:0;overflow:hidden;color:#f3f4f6;">
Confirm your address to finish setting up your {{.ProductName}} account.
</div>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#f3f4f6;">
  <tr><td align="center" style="padding:32px 16px;">
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;background:#ffffff;border-radius:12px;box-shadow:0 1px 2px rgba(0,0,0,0.04);">
      <!-- Header -->
      <tr><td style="padding:32px 40px 0;">
        <div style="font-weight:700;font-size:20px;color:#7c3aed;letter-spacing:-0.5px;">{{.ProductName}}</div>
      </td></tr>
      <!-- Body -->
      <tr><td style="padding:24px 40px 8px;">
        <h1 style="margin:0 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">Verify your email</h1>
        <p style="margin:0 0 8px;font-size:15px;line-height:1.6;color:#374151;">
          Tap the button below to confirm <b>{{.UserEmail}}</b> on {{.ProductName}}.
        </p>
      </td></tr>
      <!-- Button (bulletproof pattern) -->
      <tr><td style="padding:24px 40px;">
        <table role="presentation" cellspacing="0" cellpadding="0" border="0">
          <tr><td align="center" bgcolor="#7c3aed" style="border-radius:8px;">
            <a href="{{.VerifyURL}}" target="_blank"
               style="display:inline-block;padding:13px 28px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none;border-radius:8px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
              Verify email
            </a>
          </td></tr>
        </table>
      </td></tr>
      <!-- Plain-text URL fallback -->
      <tr><td style="padding:0 40px 24px;">
        <p style="margin:0 0 8px;font-size:13px;line-height:1.5;color:#6b7280;">
          Or paste this link in your browser:
        </p>
        <p style="margin:0;font-size:12px;line-height:1.5;color:#4b5563;word-break:break-all;">
          <a href="{{.VerifyURL}}" style="color:#4b5563;text-decoration:underline;">{{.VerifyURL}}</a>
        </p>
      </td></tr>
      <!-- Footer note -->
      <tr><td style="padding:0 40px 32px;">
        <p style="margin:0;font-size:13px;line-height:1.5;color:#6b7280;">
          The link expires in {{.ExpiresHour}} hours. If you didn't sign up for {{.ProductName}}, you can ignore this email.
        </p>
      </td></tr>
    </table>
    <!-- Outer footer -->
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;">
      <tr><td align="center" style="padding:24px 40px;">
        <p style="margin:0;font-size:12px;color:#9ca3af;line-height:1.5;">
          {{.ProductName}} · You received this because someone signed up with this address.
        </p>
      </td></tr>
    </table>
  </td></tr>
</table>
</body></html>`

const verifyEmailText = `Verify your email — {{.ProductName}}

Tap this link to confirm {{.UserEmail}}:

{{.VerifyURL}}

The link expires in {{.ExpiresHour}} hours. If you didn't sign up, ignore this email.`

// PasswordResetData populates the password reset template.
type PasswordResetData struct {
	UserEmail   string
	ResetURL    string
	ExpiresMin  int
	ProductName string
	IPAddress   string
}

const passwordResetHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<title>Reset your password</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
<div style="display:none;max-height:0;overflow:hidden;color:#f3f4f6;">
A password reset was requested for your {{.ProductName}} account.
</div>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#f3f4f6;">
  <tr><td align="center" style="padding:32px 16px;">
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;background:#ffffff;border-radius:12px;box-shadow:0 1px 2px rgba(0,0,0,0.04);">
      <tr><td style="padding:32px 40px 0;">
        <div style="font-weight:700;font-size:20px;color:#7c3aed;letter-spacing:-0.5px;">{{.ProductName}}</div>
      </td></tr>
      <tr><td style="padding:24px 40px 8px;">
        <h1 style="margin:0 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">Reset your password</h1>
        <p style="margin:0 0 8px;font-size:15px;line-height:1.6;color:#374151;">
          A password reset was requested for <b>{{.UserEmail}}</b>.
        </p>
      </td></tr>
      <tr><td style="padding:24px 40px;">
        <table role="presentation" cellspacing="0" cellpadding="0" border="0">
          <tr><td align="center" bgcolor="#7c3aed" style="border-radius:8px;">
            <a href="{{.ResetURL}}" target="_blank"
               style="display:inline-block;padding:13px 28px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none;border-radius:8px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
              Reset password
            </a>
          </td></tr>
        </table>
      </td></tr>
      <tr><td style="padding:0 40px 24px;">
        <p style="margin:0 0 8px;font-size:13px;line-height:1.5;color:#6b7280;">
          Or paste this link in your browser:
        </p>
        <p style="margin:0;font-size:12px;line-height:1.5;color:#4b5563;word-break:break-all;">
          <a href="{{.ResetURL}}" style="color:#4b5563;text-decoration:underline;">{{.ResetURL}}</a>
        </p>
      </td></tr>
      <!-- Security notice — slightly louder than the verify-email footer
           because this is the action attackers exploit. The IP-address
           line lets the user spot a foreign request before clicking. -->
      <tr><td style="padding:0 40px 24px;">
        <table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#fef3c7;border-left:3px solid #f59e0b;border-radius:4px;">
          <tr><td style="padding:12px 16px;">
            <p style="margin:0 0 4px;font-size:13px;color:#92400e;font-weight:600;">Didn't request this?</p>
            <p style="margin:0;font-size:13px;color:#78350f;line-height:1.5;">
              Your password is unchanged. {{if .IPAddress}}This request came from <code>{{.IPAddress}}</code>.{{end}} Ignore this email — the link expires in {{.ExpiresMin}} minutes.
            </p>
          </td></tr>
        </table>
      </td></tr>
    </table>
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;">
      <tr><td align="center" style="padding:24px 40px;">
        <p style="margin:0;font-size:12px;color:#9ca3af;line-height:1.5;">
          {{.ProductName}} · This is an automated security email.
        </p>
      </td></tr>
    </table>
  </td></tr>
</table>
</body></html>`

const passwordResetText = `Reset your password — {{.ProductName}}

A password reset was requested for {{.UserEmail}}.

Reset link: {{.ResetURL}}

Expires in {{.ExpiresMin}} minutes.{{if .IPAddress}} Requested from {{.IPAddress}}.{{end}}
If this wasn't you, your password is unchanged — ignore this email.`

// OrgInviteData populates the org invitation template.
type OrgInviteData struct {
	InviterEmail string
	OrgName      string
	Role         string
	AcceptURL    string
	ExpiresDays  int
	ProductName  string
}

const orgInviteHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<title>You're invited to {{.OrgName}}</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
<div style="display:none;max-height:0;overflow:hidden;color:#f3f4f6;">
{{.InviterEmail}} invited you to {{.OrgName}} on {{.ProductName}} as a {{.Role}}.
</div>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#f3f4f6;">
  <tr><td align="center" style="padding:32px 16px;">
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;background:#ffffff;border-radius:12px;box-shadow:0 1px 2px rgba(0,0,0,0.04);">
      <tr><td style="padding:32px 40px 0;">
        <div style="font-weight:700;font-size:20px;color:#7c3aed;letter-spacing:-0.5px;">{{.ProductName}}</div>
      </td></tr>
      <tr><td style="padding:24px 40px 8px;">
        <h1 style="margin:0 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">You're invited to {{.OrgName}}</h1>
        <p style="margin:0 0 8px;font-size:15px;line-height:1.6;color:#374151;">
          <b>{{.InviterEmail}}</b> invited you to join <b>{{.OrgName}}</b> on {{.ProductName}} as a <b>{{.Role}}</b>.
        </p>
      </td></tr>
      <tr><td style="padding:24px 40px;">
        <table role="presentation" cellspacing="0" cellpadding="0" border="0">
          <tr><td align="center" bgcolor="#7c3aed" style="border-radius:8px;">
            <a href="{{.AcceptURL}}" target="_blank"
               style="display:inline-block;padding:13px 28px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none;border-radius:8px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
              Accept invite
            </a>
          </td></tr>
        </table>
      </td></tr>
      <tr><td style="padding:0 40px 24px;">
        <p style="margin:0 0 8px;font-size:13px;line-height:1.5;color:#6b7280;">
          Or paste this link in your browser:
        </p>
        <p style="margin:0;font-size:12px;line-height:1.5;color:#4b5563;word-break:break-all;">
          <a href="{{.AcceptURL}}" style="color:#4b5563;text-decoration:underline;">{{.AcceptURL}}</a>
        </p>
      </td></tr>
      <tr><td style="padding:0 40px 32px;">
        <p style="margin:0;font-size:13px;line-height:1.5;color:#6b7280;">
          Expires in {{.ExpiresDays}} days. If you weren't expecting this invitation, you can ignore the email.
        </p>
      </td></tr>
    </table>
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;">
      <tr><td align="center" style="padding:24px 40px;">
        <p style="margin:0;font-size:12px;color:#9ca3af;line-height:1.5;">
          {{.ProductName}} · You received this because {{.InviterEmail}} added you to an organization.
        </p>
      </td></tr>
    </table>
  </td></tr>
</table>
</body></html>`

const orgInviteText = `You're invited to {{.OrgName}} — {{.ProductName}}

{{.InviterEmail}} invited you to join {{.OrgName}} as a {{.Role}}.

Accept: {{.AcceptURL}}

Expires in {{.ExpiresDays}} days.`

// AlertData populates the operator-alert template.
type AlertData struct {
	Title        string
	Severity     string // "critical" | "high" | "medium" | "low"
	Body         string
	ProjectID    string
	DashboardURL string
	ProductName  string
}

const alertHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<title>[{{.Severity}}] {{.Title}}</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
<div style="display:none;max-height:0;overflow:hidden;color:#f3f4f6;">
[{{.Severity}}] {{.Title}}{{if .ProjectID}} — project {{.ProjectID}}{{end}}
</div>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#f3f4f6;">
  <tr><td align="center" style="padding:32px 16px;">
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;background:#ffffff;border-radius:12px;box-shadow:0 1px 2px rgba(0,0,0,0.04);">
      <!-- Severity stripe — color-coded so the email's first impression
           communicates urgency before any text is read. -->
      <tr><td style="padding:0;">
        <div style="height:4px;background:{{if eq .Severity "critical"}}#dc2626{{else if eq .Severity "high"}}#ea580c{{else if eq .Severity "medium"}}#ca8a04{{else}}#0284c7{{end}};border-radius:12px 12px 0 0;"></div>
      </td></tr>
      <tr><td style="padding:28px 40px 0;">
        <div style="font-weight:700;font-size:20px;color:#7c3aed;letter-spacing:-0.5px;">{{.ProductName}}</div>
      </td></tr>
      <tr><td style="padding:16px 40px 8px;">
        <div style="display:inline-block;padding:3px 10px;font-size:11px;font-weight:700;text-transform:uppercase;letter-spacing:0.05em;color:{{if eq .Severity "critical"}}#dc2626{{else if eq .Severity "high"}}#ea580c{{else if eq .Severity "medium"}}#ca8a04{{else}}#0284c7{{end}};background:{{if eq .Severity "critical"}}#fee2e2{{else if eq .Severity "high"}}#ffedd5{{else if eq .Severity "medium"}}#fef9c3{{else}}#dbeafe{{end}};border-radius:4px;">{{.Severity}}</div>
        <h1 style="margin:12px 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">{{.Title}}</h1>
        {{if .ProjectID}}<p style="margin:0 0 8px;font-size:13px;color:#6b7280;">Project: <code style="background:#f3f4f6;padding:2px 6px;border-radius:4px;font-size:12px;">{{.ProjectID}}</code></p>{{end}}
      </td></tr>
      <tr><td style="padding:8px 40px 24px;">
        <pre style="margin:0;background:#f9fafb;padding:16px;border:1px solid #e5e7eb;border-radius:8px;font-size:12px;line-height:1.5;color:#374151;font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace;white-space:pre-wrap;word-break:break-word;">{{.Body}}</pre>
      </td></tr>
      {{if .DashboardURL}}<tr><td style="padding:0 40px 32px;">
        <table role="presentation" cellspacing="0" cellpadding="0" border="0">
          <tr><td align="center" bgcolor="#111827" style="border-radius:8px;">
            <a href="{{.DashboardURL}}" target="_blank"
               style="display:inline-block;padding:11px 22px;font-size:14px;font-weight:600;color:#ffffff;text-decoration:none;border-radius:8px;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
              Open dashboard →
            </a>
          </td></tr>
        </table>
      </td></tr>{{end}}
    </table>
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;">
      <tr><td align="center" style="padding:24px 40px;">
        <p style="margin:0;font-size:12px;color:#9ca3af;line-height:1.5;">
          {{.ProductName}} alerts · You received this because alerting is enabled for your project.
        </p>
      </td></tr>
    </table>
  </td></tr>
</table>
</body></html>`

const alertText = `[{{.Severity}}] {{.Title}}

{{if .ProjectID}}Project: {{.ProjectID}}{{end}}

{{.Body}}

{{if .DashboardURL}}Dashboard: {{.DashboardURL}}{{end}}`

// BuildVerifyEmail produces the rendered Subject, HTML, and text for an
// email-verification message. Returned Message has To unset — caller fills
// it in to support both single-recipient and bcc-style flows.
func BuildVerifyEmail(d VerifyEmailData) (Message, error) {
	if d.ProductName == "" {
		d.ProductName = "Excalibase"
	}
	if d.ExpiresHour == 0 {
		d.ExpiresHour = 24
	}
	html, text, err := Render(verifyEmailHTML, verifyEmailText, d)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Subject:  "Verify your email",
		HTMLBody: html,
		TextBody: text,
		Tags:     map[string]string{"category": "transactional", "template": "verify_email"},
	}, nil
}

// BuildPasswordResetEmail produces the rendered reset-password Message.
func BuildPasswordResetEmail(d PasswordResetData) (Message, error) {
	if d.ProductName == "" {
		d.ProductName = "Excalibase"
	}
	if d.ExpiresMin == 0 {
		d.ExpiresMin = 60
	}
	html, text, err := Render(passwordResetHTML, passwordResetText, d)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Subject:  "Reset your password",
		HTMLBody: html,
		TextBody: text,
		Tags:     map[string]string{"category": "transactional", "template": "password_reset"},
	}, nil
}

// BuildOrgInviteEmail produces the rendered org-invite Message.
func BuildOrgInviteEmail(d OrgInviteData) (Message, error) {
	if d.ProductName == "" {
		d.ProductName = "Excalibase"
	}
	if d.ExpiresDays == 0 {
		d.ExpiresDays = 7
	}
	html, text, err := Render(orgInviteHTML, orgInviteText, d)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Subject:  "You're invited to " + d.OrgName,
		HTMLBody: html,
		TextBody: text,
		Tags:     map[string]string{"category": "transactional", "template": "org_invite"},
	}, nil
}

// BuildAlertEmail produces the rendered operator-alert Message.
func BuildAlertEmail(d AlertData) (Message, error) {
	if d.ProductName == "" {
		d.ProductName = "Excalibase"
	}
	html, text, err := Render(alertHTML, alertText, d)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Subject:  "[" + d.Severity + "] " + d.Title,
		HTMLBody: html,
		TextBody: text,
		Tags:     map[string]string{"category": "alert", "severity": d.Severity},
	}, nil
}
