package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/go-chi/chi/v5"
)

// internalEmailSendPath is the server-to-server mail relay the auth service
// calls. It lives on the root router (not under /api) because the caller is a
// service principal, not a studio user with a session. The caller mounts it
// behind auth.RequireAuth and middleware.RequireCapability, which together
// admit only a service token granting email:send.
const internalEmailSendPath = "/internal/email/send"

// Template names accepted by the relay. Auth sends these verbatim, so the
// values are a wire contract and must not change.
const (
	emailTemplateVerify    = "verify_email"
	emailTemplateReset     = "password_reset"
	emailTemplateMagicLink = "magic_link"
	emailTemplateEmailOTP  = "email_otp"
	// An address a signed-in end user adds to their account (EXC-662).
	emailTemplateLinkMagicLink = "link_magic_link"
	emailTemplateLinkEmailOTP  = "link_email_otp"
	// Account mails (EXC-662 step 4).
	emailTemplateEmailChange       = "email_change"
	emailTemplateEmailChangeNotice = "email_change_notice"
	emailTemplateIdentityLinked    = "identity_linked"
)

// InternalEmailHandler renders a transactional template and hands it to the
// platform's configured email provider. excalibase-auth has no mail SDK of its
// own, so this endpoint is its only outbound path.
type InternalEmailHandler struct {
	sender     email.Sender
	flags      features.Flags
	signInMail SignInMailSource
}

// SignInMailSource names the project in its sign-in mails and keeps the
// platform's own daily budget of them, which the shared sender relies on.
type SignInMailSource interface {
	SignInMailProject(ctx context.Context, projectID string) (domain.SignInMailProject, error)
	// TakeSignInMail records one mail of a budget (domain.MailBudget*) if the
	// project has that budget left today.
	TakeSignInMail(ctx context.Context, projectID, kind string, perDay int) (bool, error)
}

// projectMailPerDay is a project's mails a day of each budget. The auth
// service holds each project to fewer; this bounds a caller that does not.
const projectMailPerDay = 1000

func NewInternalEmailHandler(sender email.Sender) *InternalEmailHandler {
	return &InternalEmailHandler{sender: sender}
}

// WithSignInMail lets the relay send the mails that speak for a project:
// sign-in links and codes, and account notices. Without it it refuses them.
func (h *InternalEmailHandler) WithSignInMail(flags features.Flags, source SignInMailSource) *InternalEmailHandler {
	h.flags, h.signInMail = flags, source
	return h
}

// Routes mounts POST /internal/email/send on the given router.
func (h *InternalEmailHandler) Routes(r chi.Router) {
	r.Post(internalEmailSendPath, h.Send)
}

type internalEmailRequest struct {
	ProjectID string            `json:"projectId"`
	To        string            `json:"to"`
	Template  string            `json:"template"`
	Data      map[string]string `json:"data"`
}

// Send renders the requested template and dispatches it; the caller is
// already authorized by the middleware chain this route is mounted behind.
// Logs carry only projectId + template — never the recipient, the data map,
// or any URL inside it.
func (h *InternalEmailHandler) Send(w http.ResponseWriter, r *http.Request) {
	// No provider wired at all is a deployment fault, not a dropped mail: the
	// auth service must learn that the verification it queued never went out.
	if h.sender == nil {
		httpError(w, "email relay not configured", http.StatusServiceUnavailable)
		return
	}

	var (
		req internalEmailRequest
		ok  bool
	)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "malformed request body", http.StatusBadRequest)
		return
	}
	// The project ref is the only request field that reaches the log, so it is
	// held to the same shape every other project path parameter is.
	if !isValidID(req.ProjectID) {
		httpError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.To) == "" {
		httpError(w, "missing recipient", http.StatusBadRequest)
		return
	}

	msg, template, err := buildInternalEmail(req, domain.SignInMailProject{})
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if gate, gated := projectMailGateFor(template); gated {
		if msg, ok = h.projectMessage(r.Context(), w, req, gate); !ok {
			return
		}
	}
	msg.To = []string{req.To}

	projectID := safeLog(req.ProjectID)
	if _, err := h.sender.Send(r.Context(), msg); err != nil {
		if errors.Is(err, email.ErrNotConfigured) {
			log.Printf("INFO: email provider not configured; dropping %s for %s", template, projectID)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		log.Printf("ERROR: email relay failed for %s template %s: %v", projectID, template, err)
		httpError(w, "email provider failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// projectMailGate decides whether a project may send one kind of mail: the
// flags checked before the project is read, then the project's own settings.
type projectMailGate struct {
	flagsOn func(ctx context.Context, flags features.Flags) bool
	allows  func(ctx context.Context, flags features.Flags, project domain.SignInMailProject) bool
	refusal string
	// budget is the daily budget the mail draws on; label names it in answers and logs.
	budget string
	label  string
}

var (
	signInMailGate = projectMailGate{
		flagsOn: func(ctx context.Context, flags features.Flags) bool {
			return features.Enabled(ctx, flags, features.AuthOTP)
		},
		allows: func(_ context.Context, _ features.Flags, project domain.SignInMailProject) bool {
			return project.EmailOTPEnabled
		},
		refusal: "sign-in mail is not enabled",
		budget:  domain.MailBudgetSignIn,
		label:   "sign-in",
	}
	emailChangeMailGate = projectMailGate{
		flagsOn: func(ctx context.Context, flags features.Flags) bool {
			return features.Enabled(ctx, flags, features.AuthUser)
		},
		allows: func(_ context.Context, _ features.Flags, project domain.SignInMailProject) bool {
			return project.Exists
		},
		refusal: "account mail is not enabled",
		budget:  domain.MailBudgetAccount,
		label:   "account",
	}
	// A method can be added only while some sign-in method that adds one is in use.
	identityLinkedMailGate = projectMailGate{
		flagsOn: func(ctx context.Context, flags features.Flags) bool {
			return features.Enabled(ctx, flags, features.AuthOAuth) || features.Enabled(ctx, flags, features.AuthOTP)
		},
		allows: func(ctx context.Context, flags features.Flags, project domain.SignInMailProject) bool {
			return (features.Enabled(ctx, flags, features.AuthOAuth) && project.OAuthEnabled) ||
				(features.Enabled(ctx, flags, features.AuthOTP) && project.EmailOTPEnabled)
		},
		refusal: "account mail is not enabled",
		budget:  domain.MailBudgetAccount,
		label:   "account",
	}
)

// projectMailGateFor names the gate of a template that speaks for a project;
// verify_email and password_reset have none.
func projectMailGateFor(template string) (projectMailGate, bool) {
	switch template {
	case emailTemplateMagicLink, emailTemplateEmailOTP, emailTemplateLinkMagicLink, emailTemplateLinkEmailOTP:
		return signInMailGate, true
	case emailTemplateEmailChange, emailTemplateEmailChangeNotice:
		return emailChangeMailGate, true
	case emailTemplateIdentityLinked:
		return identityLinkedMailGate, true
	}
	return projectMailGate{}, false
}

// projectMessage renders a mail named for its project, once the project may
// send it and has budget left; otherwise it answers itself.
func (h *InternalEmailHandler) projectMessage(ctx context.Context, w http.ResponseWriter, req internalEmailRequest, gate projectMailGate) (email.Message, bool) {
	if h.signInMail == nil || !gate.flagsOn(ctx, h.flags) {
		httpError(w, gate.refusal, http.StatusForbidden)
		return email.Message{}, false
	}
	project, err := h.signInMail.SignInMailProject(ctx, req.ProjectID)
	if err != nil {
		httpError(w, "sign-in settings unavailable", http.StatusServiceUnavailable)
		return email.Message{}, false
	}
	if !gate.allows(ctx, h.flags, project) {
		httpError(w, gate.refusal, http.StatusForbidden)
		return email.Message{}, false
	}
	msg, _, err := buildInternalEmail(req, project)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return email.Message{}, false
	}
	taken, err := h.signInMail.TakeSignInMail(ctx, req.ProjectID, gate.budget, projectMailPerDay)
	switch {
	case err != nil:
		httpError(w, gate.label+" mail budget unavailable", http.StatusServiceUnavailable)
		return email.Message{}, false
	case !taken:
		log.Printf("WARN: %s mail budget spent for %s", gate.label, safeLog(req.ProjectID))
		httpError(w, gate.label+" mail budget spent for today", http.StatusTooManyRequests)
		return email.Message{}, false
	}
	return msg, true
}

// buildInternalEmail dispatches on the template name. Unknown or missing
// templates are a client error, never a silent no-op. The template name it
// returns is the matched constant, not the request string, so callers can log
// it without carrying the caller's bytes along.
func buildInternalEmail(req internalEmailRequest, project domain.SignInMailProject) (email.Message, string, error) {
	switch req.Template {
	case emailTemplateVerify:
		msg, err := email.BuildVerifyEmail(email.VerifyEmailData{
			UserEmail:   req.Data["userEmail"],
			VerifyURL:   req.Data["verifyUrl"],
			ExpiresHour: atoiOrZero(req.Data["expiresHour"]),
		})
		return msg, emailTemplateVerify, err
	case emailTemplateReset:
		msg, err := email.BuildPasswordResetEmail(email.PasswordResetData{
			UserEmail:  req.Data["userEmail"],
			ResetURL:   req.Data["resetUrl"],
			ExpiresMin: atoiOrZero(req.Data["expiresMin"]),
		})
		return msg, emailTemplateReset, err
	case emailTemplateMagicLink, emailTemplateLinkMagicLink:
		build, template := email.BuildMagicLinkEmail, emailTemplateMagicLink
		if req.Template == emailTemplateLinkMagicLink {
			build, template = email.BuildLinkMagicLinkEmail, emailTemplateLinkMagicLink
		}
		msg, err := build(email.MagicLinkData{
			UserEmail:   req.Data["userEmail"],
			LinkURL:     req.Data["linkUrl"],
			ExpiresMin:  atoiOrZero(req.Data["expiresMin"]),
			ProductName: project.Name,
			SiteURL:     project.SiteURL,
		})
		return msg, template, err
	case emailTemplateEmailOTP, emailTemplateLinkEmailOTP:
		build, template := email.BuildEmailOTPEmail, emailTemplateEmailOTP
		if req.Template == emailTemplateLinkEmailOTP {
			build, template = email.BuildLinkEmailOTPEmail, emailTemplateLinkEmailOTP
		}
		msg, err := build(email.EmailOTPData{
			UserEmail:   req.Data["userEmail"],
			Code:        req.Data["code"],
			ExpiresMin:  atoiOrZero(req.Data["expiresMin"]),
			ProductName: project.Name,
			SiteURL:     project.SiteURL,
		})
		return msg, template, err
	default:
		return buildAccountEmail(req, project)
	}
}

// buildAccountEmail renders the account mails; their data is checked by the builders.
func buildAccountEmail(req internalEmailRequest, project domain.SignInMailProject) (email.Message, string, error) {
	switch req.Template {
	case emailTemplateEmailChange:
		msg, err := email.BuildEmailChangeEmail(email.MagicLinkData{
			UserEmail: req.Data["userEmail"], LinkURL: req.Data["linkUrl"], ExpiresMin: atoiOrZero(req.Data["expiresMin"]),
			ProductName: project.Name, SiteURL: project.SiteURL,
		})
		return msg, emailTemplateEmailChange, err
	case emailTemplateEmailChangeNotice:
		msg, err := email.BuildEmailChangeNoticeEmail(email.EmailChangeNoticeData{
			UserEmail: req.Data["userEmail"], NewEmail: req.Data["newEmail"], ProductName: project.Name, SiteURL: project.SiteURL,
		})
		return msg, emailTemplateEmailChangeNotice, err
	case emailTemplateIdentityLinked:
		msg, err := email.BuildIdentityLinkedEmail(email.IdentityLinkedData{
			UserEmail: req.Data["userEmail"], Method: req.Data["method"], LinkedEmail: req.Data["linkedEmail"],
			ProductName: project.Name, SiteURL: project.SiteURL,
		})
		return msg, emailTemplateIdentityLinked, err
	default:
		return email.Message{}, "", errors.New("unknown or missing template")
	}
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
