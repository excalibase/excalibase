package email

import (
	"errors"
	"net/mail"
	"strings"
)

// End-user account mails (EXC-662): confirming a new address, telling the old
// address it is being replaced, and telling an account a sign-in method was
// added. Each names the project the way the sign-in mails do.

// EmailChangeNoticeData populates the notice sent to the address being replaced.
type EmailChangeNoticeData struct {
	UserEmail   string
	NewEmail    string
	ProductName string
	SiteURL     string
	SiteHost    string
}

// IdentityLinkedData populates the notice that a sign-in method was added.
// Method is google, github or email; LinkedEmail may be empty.
type IdentityLinkedData struct {
	UserEmail   string
	Method      string
	LinkedEmail string
	ProductName string
	SiteURL     string
	SiteHost    string
	MethodLabel string
}

var (
	errAccountAddress = errors.New("email address must be a plain address")
	errAccountMethod  = errors.New("method must be google, github or email")
)

// The sign-in head's title and preheader say "sign in"; these mails are not one.
var accountHTMLHead = strings.NewReplacer(
	"<title>Sign in</title>", "<title>Your account</title>",
	"Your sign-in to {{.ProductName}}.", "About your {{.ProductName}} account.",
).Replace(signInHTMLHead)

const accountHTMLSignature = `
        <p style="margin:12px 0 0;font-size:12px;line-height:1.5;color:#9ca3af;">
          Sent by Excalibase on behalf of {{.ProductName}}.
        </p>
      </td></tr>
    </table>
  </td></tr>
</table>
</body></html>`

var emailChangeHTML = accountHTMLHead + `
      <tr><td style="padding:24px 40px 8px;">
        <h1 style="margin:0 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">Confirm your new email address</h1>
        <p style="margin:0 0 8px;font-size:15px;line-height:1.6;color:#374151;">
          Tap the button below to make <b>{{.UserEmail}}</b> the sign-in address of an account.
        </p>
      </td></tr>
      <tr><td style="padding:24px 40px;">
        <table role="presentation" cellspacing="0" cellpadding="0" border="0">
          <tr><td align="center" bgcolor="#7c3aed" style="border-radius:8px;">
            <a href="{{.LinkURL}}" target="_blank"
               style="display:inline-block;padding:13px 28px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none;border-radius:8px;">
              Confirm this address
            </a>
          </td></tr>
        </table>
      </td></tr>
      <tr><td style="padding:0 40px 24px;">
        <p style="margin:0 0 8px;font-size:13px;line-height:1.5;color:#6b7280;">Or paste this link in your browser:</p>
        <p style="margin:0;font-size:12px;line-height:1.5;color:#4b5563;word-break:break-all;">
          <a href="{{.LinkURL}}" style="color:#4b5563;text-decoration:underline;">{{.LinkURL}}</a>
        </p>
      </td></tr>
      <tr><td style="padding:0 40px 32px;">
        <p style="margin:0;font-size:13px;line-height:1.5;color:#6b7280;">
          It expires in {{.ExpiresMin}} minutes and works once. If you didn't ask for this, you can ignore this email.
        </p>` + accountHTMLSignature

const emailChangeText = `Confirm your new email address — {{.ProductName}}{{if .SiteHost}} ({{.SiteHost}}){{end}}

Open this link to make {{.UserEmail}} the sign-in address of an account:

{{.LinkURL}}

It expires in {{.ExpiresMin}} minutes and works once. If you didn't ask for this, ignore this email.

Sent by Excalibase on behalf of {{.ProductName}}.`

var emailChangeNoticeHTML = accountHTMLHead + `
      <tr><td style="padding:24px 40px 8px;">
        <h1 style="margin:0 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">Your email address is being changed</h1>
        <p style="margin:0 0 8px;font-size:15px;line-height:1.6;color:#374151;">
          Someone signed in to your account ({{.UserEmail}}) asked to change its address to <b>{{.NewEmail}}</b>.
          Nothing changes until the new address confirms.
        </p>
      </td></tr>
      <tr><td style="padding:16px 40px 32px;">
        <p style="margin:0;font-size:13px;line-height:1.5;color:#6b7280;">
          If this wasn't you, sign in and change your password.
        </p>` + accountHTMLSignature

const emailChangeNoticeText = `Your email address is being changed — {{.ProductName}}{{if .SiteHost}} ({{.SiteHost}}){{end}}

Someone signed in to your account ({{.UserEmail}}) asked to change its address to {{.NewEmail}}. Nothing changes until the new address confirms.

If this wasn't you, sign in and change your password.

Sent by Excalibase on behalf of {{.ProductName}}.`

var identityLinkedHTML = accountHTMLHead + `
      <tr><td style="padding:24px 40px 8px;">
        <h1 style="margin:0 0 16px;font-size:22px;line-height:1.3;color:#111827;font-weight:600;">A new sign-in method was added</h1>
        <p style="margin:0 0 8px;font-size:15px;line-height:1.6;color:#374151;">
          {{.MethodLabel}} can now sign in to your account ({{.UserEmail}}).
        </p>
      </td></tr>
      <tr><td style="padding:16px 40px 32px;">
        <p style="margin:0;font-size:13px;line-height:1.5;color:#6b7280;">
          If this wasn't you, sign in, remove it from your account and change your password.
        </p>` + accountHTMLSignature

const identityLinkedText = `A new sign-in method was added — {{.ProductName}}{{if .SiteHost}} ({{.SiteHost}}){{end}}

{{.MethodLabel}} can now sign in to your account ({{.UserEmail}}).

If this wasn't you, sign in, remove it from your account and change your password.

Sent by Excalibase on behalf of {{.ProductName}}.`

// BuildEmailChangeEmail renders the link that confirms an account's new address.
func BuildEmailChangeEmail(d MagicLinkData) (Message, error) {
	return buildLinkMail(d, emailChangeHTML, emailChangeText, "Confirm your new email address for ", "email_change")
}

// BuildEmailChangeNoticeEmail renders the notice to the address being replaced.
func BuildEmailChangeNoticeEmail(d EmailChangeNoticeData) (Message, error) {
	if !plainAddress(d.NewEmail) {
		return Message{}, errAccountAddress
	}
	d.ProductName, _ = signInDefaults(d.ProductName, 0)
	d.SiteHost = siteHost(d.SiteURL)
	subject := "Your email address for " + d.ProductName + " is being changed"
	return renderSignIn(emailChangeNoticeHTML, emailChangeNoticeText, subject, "email_change_notice", d.ProductName, d)
}

// BuildIdentityLinkedEmail renders the notice that a sign-in method was added.
func BuildIdentityLinkedEmail(d IdentityLinkedData) (Message, error) {
	if d.LinkedEmail != "" && !plainAddress(d.LinkedEmail) {
		return Message{}, errAccountAddress
	}
	label, ok := methodLabel(d.Method, d.LinkedEmail)
	if !ok {
		return Message{}, errAccountMethod
	}
	d.MethodLabel = label
	d.ProductName, _ = signInDefaults(d.ProductName, 0)
	d.SiteHost = siteHost(d.SiteURL)
	subject := "A new sign-in method was added to your " + d.ProductName + " account"
	return renderSignIn(identityLinkedHTML, identityLinkedText, subject, "identity_linked", d.ProductName, d)
}

func methodLabel(method, linkedEmail string) (string, bool) {
	switch method {
	case "google":
		return "Google", true
	case "github":
		return "GitHub", true
	case "email":
		if linkedEmail == "" {
			return "An email address", true
		}
		return "The email address " + linkedEmail, true
	}
	return "", false
}

// plainAddress accepts a bare address only, so no display name or header
// text rides along into the mail.
func plainAddress(address string) bool {
	parsed, err := mail.ParseAddress(address)
	return err == nil && parsed.Name == "" && parsed.Address == address
}
