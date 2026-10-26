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
	// TakeSignInMail records one sign-in mail if the project has budget left today.
	TakeSignInMail(ctx context.Context, projectID string, perDay int) (bool, error)
}

// signInMailPerDay is a project's sign-in mails a day. The auth service holds
// each project to fewer; this bounds a caller that does not.
const signInMailPerDay = 1000

func NewInternalEmailHandler(sender email.Sender) *InternalEmailHandler {
	return &InternalEmailHandler{sender: sender}
}

// WithSignInMail lets the relay send sign-in links and codes; without it, or
// with authotp off, it refuses them.
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
	if isSignInTemplate(template) {
		if msg, ok = h.signInMessage(r.Context(), w, req); !ok {
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

func isSignInTemplate(template string) bool {
	return template == emailTemplateMagicLink || template == emailTemplateEmailOTP
}

// signInMessage renders a sign-in mail named for its project, once the
// project may send one and has budget left; otherwise it answers itself.
func (h *InternalEmailHandler) signInMessage(ctx context.Context, w http.ResponseWriter, req internalEmailRequest) (email.Message, bool) {
	if h.signInMail == nil || !features.Enabled(ctx, h.flags, features.AuthOTP) {
		httpError(w, "sign-in mail is not enabled", http.StatusForbidden)
		return email.Message{}, false
	}
	project, err := h.signInMail.SignInMailProject(ctx, req.ProjectID)
	if err != nil {
		httpError(w, "sign-in settings unavailable", http.StatusServiceUnavailable)
		return email.Message{}, false
	}
	if !project.EmailOTPEnabled {
		httpError(w, "sign-in mail is not enabled", http.StatusForbidden)
		return email.Message{}, false
	}
	msg, _, err := buildInternalEmail(req, project)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return email.Message{}, false
	}
	taken, err := h.signInMail.TakeSignInMail(ctx, req.ProjectID, signInMailPerDay)
	switch {
	case err != nil:
		httpError(w, "sign-in mail budget unavailable", http.StatusServiceUnavailable)
		return email.Message{}, false
	case !taken:
		log.Printf("WARN: sign-in mail budget spent for %s", safeLog(req.ProjectID))
		httpError(w, "sign-in mail budget spent for today", http.StatusTooManyRequests)
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
	case emailTemplateMagicLink:
		msg, err := email.BuildMagicLinkEmail(email.MagicLinkData{
			UserEmail:   req.Data["userEmail"],
			LinkURL:     req.Data["linkUrl"],
			ExpiresMin:  atoiOrZero(req.Data["expiresMin"]),
			ProductName: project.Name,
			SiteURL:     project.SiteURL,
		})
		return msg, emailTemplateMagicLink, err
	case emailTemplateEmailOTP:
		msg, err := email.BuildEmailOTPEmail(email.EmailOTPData{
			UserEmail:   req.Data["userEmail"],
			Code:        req.Data["code"],
			ExpiresMin:  atoiOrZero(req.Data["expiresMin"]),
			ProductName: project.Name,
			SiteURL:     project.SiteURL,
		})
		return msg, emailTemplateEmailOTP, err
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
