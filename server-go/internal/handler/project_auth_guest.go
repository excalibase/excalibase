package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	auditActionGuestCaptchaUpdate = "auth_guest_captcha.update"
	auditActionGuestCaptchaDelete = "auth_guest_captcha.delete"
	guestCaptchaSecretField       = "secret_key"
)

type guestCaptchaRequest struct {
	SecretKey string `json:"secretKey"`
}

// PutGuestCaptcha serves PUT /api/projects/{projectId}/auth-settings/guest-captcha.
// The vault write comes first, so the row never claims a secret auth cannot read.
func (h *ProvisioningHandler) PutGuestCaptcha(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.guestCaptchaProject(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var body guestCaptchaRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	if err := domain.ValidateGuestCaptchaSecret(body.SecretKey); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.authOAuth.Secrets.Put(domain.GuestCaptchaSecretPath(projectID), map[string]string{guestCaptchaSecretField: body.SecretKey}); err != nil {
		log.Printf("ERROR: store the Turnstile secret of project %s: %v", projectID, err)
		httpError(w, "failed to store the secret", http.StatusInternalServerError)
		return
	}
	h.finishGuestCaptcha(w, r, projectID, true, auditActionGuestCaptchaUpdate)
}

// DeleteGuestCaptcha serves DELETE on the same path: the row goes first, so
// auth stops asking for a captcha before the secret disappears.
func (h *ProvisioningHandler) DeleteGuestCaptcha(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.guestCaptchaProject(w, r)
	if !ok {
		return
	}
	if err := h.authOAuth.SignIn.SetGuestCaptchaSecretSet(r.Context(), projectID, false); err != nil {
		log.Printf("ERROR: clear the guest captcha of project %s: %v", projectID, err)
		httpError(w, "failed to save the setting", http.StatusInternalServerError)
		return
	}
	if err := h.authOAuth.Secrets.Delete(domain.GuestCaptchaSecretPath(projectID)); err != nil {
		log.Printf("ERROR: delete the Turnstile secret of project %s: %v", projectID, err)
		httpError(w, "failed to delete the secret", http.StatusInternalServerError)
		return
	}
	h.finishGuestCaptcha(w, r, projectID, false, auditActionGuestCaptchaDelete)
}

func (h *ProvisioningHandler) finishGuestCaptcha(w http.ResponseWriter, r *http.Request, projectID string, set bool, action string) {
	if set {
		if err := h.authOAuth.SignIn.SetGuestCaptchaSecretSet(r.Context(), projectID, true); err != nil {
			log.Printf("ERROR: mark the guest captcha of project %s: %v", projectID, err)
			httpError(w, "failed to save the setting", http.StatusInternalServerError)
			return
		}
	}
	h.writeAuthAudit(r, projectID, action, auditResourceAuthSettings, projectID, `{"guestCaptchaSecretSet":`+strconv.FormatBool(set)+`}`)
	signIn, err := h.storedAuthSignIn(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, h.authSignInResponseFor(r.Context(), signIn))
}

// guestCaptchaProject validates the project and the wiring; the route's flag
// gate answers 404 while authguest is off, checked here again to fail closed.
func (h *ProvisioningHandler) guestCaptchaProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID, ok := h.authSettingsRequestProject(w, r)
	if !ok {
		return "", false
	}
	if !h.authGuestOn(r.Context()) {
		http.NotFound(w, r)
		return "", false
	}
	if h.authOAuth.SignIn == nil || h.authOAuth.Secrets == nil {
		httpError(w, "the vault is not available", http.StatusServiceUnavailable)
		return "", false
	}
	return projectID, true
}
