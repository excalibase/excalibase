package handler

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/email"
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
	emailTemplateVerify = "verify_email"
	emailTemplateReset  = "password_reset"
)

// InternalEmailHandler renders a transactional template and hands it to the
// platform's configured email provider. excalibase-auth has no mail SDK of its
// own, so this endpoint is its only outbound path.
type InternalEmailHandler struct {
	sender email.Sender
}

func NewInternalEmailHandler(sender email.Sender) *InternalEmailHandler {
	return &InternalEmailHandler{sender: sender}
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

	var req internalEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "malformed request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.To) == "" {
		httpError(w, "missing recipient", http.StatusBadRequest)
		return
	}

	msg, err := buildInternalEmail(req)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	msg.To = []string{req.To}

	if _, err := h.sender.Send(r.Context(), msg); err != nil {
		if errors.Is(err, email.ErrNotConfigured) {
			log.Printf("INFO: email provider not configured; dropping %s for %s", req.Template, req.ProjectID)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		log.Printf("ERROR: email relay failed for %s template %s: %v", req.ProjectID, req.Template, err)
		httpError(w, "email provider failed", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// buildInternalEmail dispatches on the template name. Unknown or missing
// templates are a client error, never a silent no-op.
func buildInternalEmail(req internalEmailRequest) (email.Message, error) {
	switch req.Template {
	case emailTemplateVerify:
		return email.BuildVerifyEmail(email.VerifyEmailData{
			UserEmail:   req.Data["userEmail"],
			VerifyURL:   req.Data["verifyUrl"],
			ExpiresHour: atoiOrZero(req.Data["expiresHour"]),
		})
	case emailTemplateReset:
		return email.BuildPasswordResetEmail(email.PasswordResetData{
			UserEmail:  req.Data["userEmail"],
			ResetURL:   req.Data["resetUrl"],
			ExpiresMin: atoiOrZero(req.Data["expiresMin"]),
		})
	default:
		return email.Message{}, errors.New("unknown or missing template")
	}
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
