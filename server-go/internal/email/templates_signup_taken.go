package email

// AccountExistsData populates the notice a sign-up sends when its address, or
// its username, already belongs to an account. The sign-up form itself answers
// the same either way, so this mail is the only place the outcome is said.
type AccountExistsData struct {
	UserEmail string
	// UsernameTaken is true when the address is new but the chosen username is not.
	UsernameTaken bool
	SignInURL     string
	ProductName   string
}

const accountExistsHTML = `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>{{.ProductName}} sign-up</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Helvetica,Arial,sans-serif;color:#111827;">
<div style="max-width:560px;margin:24px auto;padding:0 16px;">
<div style="font-weight:700;font-size:20px;color:#7c3aed;">{{.ProductName}}</div>
{{if .UsernameTaken}}<p>Someone asked to create a {{.ProductName}} account for {{.UserEmail}}, but that username is already taken.</p>
<p>No account was created. Sign up again with a different username{{if .SignInURL}}, or <a href="{{.SignInURL}}">sign in</a> if you already have an account{{end}}.</p>
{{else}}<p>Someone asked to create a {{.ProductName}} account with {{.UserEmail}}, which already has one.</p>
<p>No new account was created.{{if .SignInURL}} <a href="{{.SignInURL}}">Sign in</a>, or reset your password if you have forgotten it.{{end}}</p>
{{end}}<p style="color:#6b7280;font-size:13px;">If this was not you, ignore this message.</p>
</div></body></html>`

const accountExistsText = `{{if .UsernameTaken}}Someone asked to create a {{.ProductName}} account for {{.UserEmail}}, but that username is already taken.

No account was created. Sign up again with a different username.{{if .SignInURL}} If you already have an account, sign in at {{.SignInURL}}.{{end}}
{{else}}Someone asked to create a {{.ProductName}} account with {{.UserEmail}}, which already has one.

No new account was created.{{if .SignInURL}} Sign in at {{.SignInURL}}, or reset your password if you have forgotten it.{{end}}
{{end}}
If this was not you, ignore this message.
`

// BuildAccountExistsEmail renders the notice. To is left for the caller.
func BuildAccountExistsEmail(d AccountExistsData) (Message, error) {
	if d.ProductName == "" {
		d.ProductName = "Excalibase"
	}
	html, text, err := Render(accountExistsHTML, accountExistsText, d)
	if err != nil {
		return Message{}, err
	}
	subject := "You already have an account"
	if d.UsernameTaken {
		subject = "That username is taken"
	}
	return Message{
		Subject: subject, HTMLBody: html, TextBody: text,
		Tags: map[string]string{"category": "transactional", "template": "account_exists"},
	}, nil
}
