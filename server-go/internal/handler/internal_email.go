package handler

import (
	"crypto/subtle"
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
// service holding the platform PAT, not a studio user with a session.
const internalEmailSendPath = "/internal/email/send"

// Template names accepted by the relay. Auth sends these verbatim.
const (
	templateVerifyEmail   = "verify_email"
	templatePasswordReset = "password_reset"
)

// InternalEmailHandler renders a transactional template and hands it to the
// platform's configured email provider. excalibase-auth has no mail SDK of its
// own, so this endpoint is its only outbound path.
type InternalEmailHandler struct {
	sender email.Sender
	pat    string
}

// NewInternalEmailHandler builds the relay. An empty pat disables the route
// (every request answers 503) — it must never be open.
func NewInternalEmailHandler(sender email.Sender, pat string) *InternalEmailHandler {
	return &InternalEmailHandler{sender: sender, pat: pat}
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

// Send authenticates the service PAT, renders the requested template and
// dispatches it. Logs carry only projectId + template — never the recipient,
// the data map, or any URL inside it.
func (h *InternalEmailHandler) Send(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
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

	if h.sender == nil {
		log.Printf("INFO: email relay has no provider; dropping %s for %s", req.Template, req.ProjectID)
		w.WriteHeader(http.StatusAccepted)
		return
	}
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

// authorize compares the bearer token against the platform service PAT in
// constant time. Fails closed when no PAT is configured.
func (h *InternalEmailHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	if h.pat == "" {
		httpError(w, "email relay not configured", http.StatusServiceUnavailable)
		return false
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	candidate := []byte(strings.TrimPrefix(auth, "Bearer "))
	if subtle.ConstantTimeCompare(candidate, []byte(h.pat)) != 1 {
		httpError(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

// buildInternalEmail dispatches on the template name. Unknown or missing
// templates are a client error, never a silent no-op.
func buildInternalEmail(req internalEmailRequest) (email.Message, error) {
	switch req.Template {
	case templateVerifyEmail:
		return email.BuildVerifyEmail(email.VerifyEmailData{
			UserEmail:   req.Data["userEmail"],
			VerifyURL:   req.Data["verifyUrl"],
			ExpiresHour: atoiOrZero(req.Data["expiresHour"]),
		})
	case templatePasswordReset:
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
