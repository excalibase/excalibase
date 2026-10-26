package email

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// End-user sign-in by emailed link or code (EXC-662). The auth service sends
// both through the relay; the link points at the project's own site.

const defaultSignInExpiresMin = 10

// MagicLinkData populates the magic-link template. ProductName and SiteURL
// name the project, so the reader knows which app is asking.
type MagicLinkData struct {
	UserEmail   string
	LinkURL     string
	ExpiresMin  int
	ProductName string
	SiteURL     string
	SiteHost    string
}

// EmailOTPData populates the six-digit code template.
type EmailOTPData struct {
	UserEmail   string
	Code        string
	ExpiresMin  int
	ProductName string
	SiteURL     string
	SiteHost    string
}

var (
	errSignInLink = errors.New("magic link must be an absolute http(s) URL")
	errSignInCode = errors.New("sign-in code must be six digits")
)

const signInHTMLHead = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="x-apple-disable-message-reformatting">
<title>Sign in</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;">
<div style="display:none;max-height:0;overflow:hidden;color:#f3f4f6;">
Your sign-in to {{.ProductName}}.
</div>
<table role="presentation" width="100%" cellspacing="0" cellpadding="0" border="0" style="background:#f3f4f6;">
  <tr><td align="center" style="padding:32px 16px;">
    <table role="presentation" width="600" cellspacing="0" cellpadding="0" border="0" style="max-width:600px;background:#ffffff;border-radius:12px;box-shadow:0 1px 2px rgba(0,0,0,0.04);">
      <tr><td style="padding:32px 40px 0;">
        <div style="font-weight:700;font-size:20px;color:#7c3aed;letter-spacing:-0.5px;">{{.ProductName}}</div>
        {{if .SiteHost}}<div style="font-size:13px;color:#6b7280;">{{.SiteHost}}</div>{{end}}
      </td></tr>`

const signInHTMLFoot = `
      <tr><td style="padding:0 40px 32px;">
        <p style="margin:0;font-size:13px;line-height:1.5;color:#6b7280;">
          It expires in {{.ExpiresMin}} minutes and works once. If you didn't ask to sign in, you can ignore this email.
        </p>
      </td></tr>
    </table>
  </td></tr>
</table>
</body></html>`

const magicLinkHTML = signInHTMLHead + `
      <tr><td style="padding:24px 40px 8px;">
        <h1 style="margin:0 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">Sign in</h1>
        <p style="margin:0 0 8px;font-size:15px;line-height:1.6;color:#374151;">
          Tap the button below to sign in as <b>{{.UserEmail}}</b>, in the browser where you asked for this email.
        </p>
      </td></tr>
      <tr><td style="padding:24px 40px;">
        <table role="presentation" cellspacing="0" cellpadding="0" border="0">
          <tr><td align="center" bgcolor="#7c3aed" style="border-radius:8px;">
            <a href="{{.LinkURL}}" target="_blank"
               style="display:inline-block;padding:13px 28px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none;border-radius:8px;">
              Sign in
            </a>
          </td></tr>
        </table>
      </td></tr>
      <tr><td style="padding:0 40px 24px;">
        <p style="margin:0 0 8px;font-size:13px;line-height:1.5;color:#6b7280;">Or paste this link in your browser:</p>
        <p style="margin:0;font-size:12px;line-height:1.5;color:#4b5563;word-break:break-all;">
          <a href="{{.LinkURL}}" style="color:#4b5563;text-decoration:underline;">{{.LinkURL}}</a>
        </p>
      </td></tr>` + signInHTMLFoot

const magicLinkText = `Sign in — {{.ProductName}}{{if .SiteHost}} ({{.SiteHost}}){{end}}

Open this link to sign in as {{.UserEmail}}, in the browser where you asked for this email:

{{.LinkURL}}

It expires in {{.ExpiresMin}} minutes and works once. If you didn't ask to sign in, ignore this email.`

const emailOTPHTML = signInHTMLHead + `
      <tr><td style="padding:24px 40px 8px;">
        <h1 style="margin:0 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">Your sign-in code</h1>
        <p style="margin:0 0 8px;font-size:15px;line-height:1.6;color:#374151;">
          Enter this code to sign in as <b>{{.UserEmail}}</b>:
        </p>
      </td></tr>
      <tr><td style="padding:16px 40px 24px;">
        <div style="font-size:32px;font-weight:700;letter-spacing:8px;color:#111827;font-family:Menlo,Consolas,monospace;">{{.Code}}</div>
      </td></tr>` + signInHTMLFoot

const emailOTPText = `Your sign-in code — {{.ProductName}}{{if .SiteHost}} ({{.SiteHost}}){{end}}

Enter this code to sign in as {{.UserEmail}}:

{{.Code}}

It expires in {{.ExpiresMin}} minutes and works once. If you didn't ask to sign in, ignore this email.`

// BuildMagicLinkEmail renders the sign-in link message.
func BuildMagicLinkEmail(d MagicLinkData) (Message, error) {
	parsed, err := url.Parse(d.LinkURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return Message{}, errSignInLink
	}
	d.ProductName, d.ExpiresMin = signInDefaults(d.ProductName, d.ExpiresMin)
	d.SiteHost = siteHost(d.SiteURL)
	return renderSignIn(magicLinkHTML, magicLinkText, "Your sign-in link for "+d.ProductName, "magic_link", d)
}

// BuildEmailOTPEmail renders the sign-in code message; the code stays out of
// the subject, which lock screens show.
func BuildEmailOTPEmail(d EmailOTPData) (Message, error) {
	if !sixDigits(d.Code) {
		return Message{}, errSignInCode
	}
	d.ProductName, d.ExpiresMin = signInDefaults(d.ProductName, d.ExpiresMin)
	d.SiteHost = siteHost(d.SiteURL)
	return renderSignIn(emailOTPHTML, emailOTPText, "Your sign-in code for "+d.ProductName, "email_otp", d)
}

// maxProductName keeps a project name from crowding the subject line.
const maxProductName = 64

func signInDefaults(product string, expiresMin int) (string, int) {
	product = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, product))
	if runes := []rune(product); len(runes) > maxProductName {
		product = string(runes[:maxProductName])
	}
	if product == "" {
		product = "Excalibase"
	}
	if expiresMin <= 0 {
		expiresMin = defaultSignInExpiresMin
	}
	return product, expiresMin
}

// siteHost is the host of the project's site, shown so a reader can tell a
// mail asked for by an app they use; anything but http(s) shows nothing.
func siteHost(site string) string {
	parsed, err := url.Parse(site)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return ""
	}
	return parsed.Host
}

func renderSignIn(html, text, subject, template string, data any) (Message, error) {
	htmlBody, textBody, err := Render(html, text, data)
	if err != nil {
		return Message{}, err
	}
	return Message{
		Subject:  subject,
		HTMLBody: htmlBody,
		TextBody: textBody,
		Tags:     map[string]string{"category": "transactional", "template": template},
	}, nil
}

func sixDigits(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
