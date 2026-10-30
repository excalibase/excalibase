package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// End-user TOTP MFA (EXC-662 step 7): the project's switch and the vault key
// the auth service seals each factor's secret with. The key is never
// answered, logged or audited, and never replaced once it exists.

const auditActionAuthMFAUpdate = "auth_mfa.update"

var (
	errMFABody          = errors.New(`body must be {"enabled": true|false}`)
	errMFAVaultReadable = errors.New("the vault could not be read")
	errMFAKeyWrite      = errors.New("the MFA key could not be stored")
)

type mfaRequest struct {
	Enabled *bool `json:"enabled"`
}

func (h *ProvisioningHandler) authMFAOn(ctx context.Context) bool {
	return features.Enabled(ctx, h.authOAuth.Flags, features.AuthMFA)
}

// PutAuthMFA serves PUT /api/projects/{projectId}/auth-settings/mfa. Enabling
// makes sure the vault holds the key before the row says MFA is on.
func (h *ProvisioningHandler) PutAuthMFA(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authMFAProject(w, r)
	if !ok {
		return
	}
	enabled, err := decodeMFARequest(w, r)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if enabled {
		err = h.authOAuth.MFA.EnableMFA(r.Context(), projectID, func() error { return h.ensureMFAKey(projectID) })
	} else {
		err = h.authOAuth.MFA.DisableMFA(r.Context(), projectID)
	}
	if err != nil {
		writeMFAError(w, projectID, err)
		return
	}
	h.writeAuthAudit(r, projectID, auditActionAuthMFAUpdate, auditResourceAuthSettings, projectID, `{"mfaEnabled":`+strconv.FormatBool(enabled)+`}`)
	view, err := h.authSettingsView(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, view)
}

func decodeMFARequest(w http.ResponseWriter, r *http.Request) (bool, error) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	var body mfaRequest
	if err := decoder.Decode(&body); err != nil || body.Enabled == nil {
		return false, errMFABody
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return false, errMFABody
	}
	return *body.Enabled, nil
}

// ensureMFAKey creates the key only when the vault says there is none; an
// unreadable vault or a malformed key is an error, never a reason to write.
func (h *ProvisioningHandler) ensureMFAKey(projectID string) error {
	path := domain.MFAKeyPath(projectID)
	stored, err := h.authOAuth.Secrets.Get(path)
	switch {
	case err == nil:
		if domain.ValidMFAKey(stored[domain.MFAKeyField]) {
			return nil
		}
		return domain.ErrInvalidMFAKey
	case !errors.Is(err, vault.ErrNotFound):
		log.Printf("ERROR: read the MFA key of project %s: %v", projectID, err)
		return errMFAVaultReadable
	}
	key, err := domain.NewMFAKey()
	if err != nil {
		return err
	}
	if err := h.authOAuth.Secrets.Put(path, map[string]string{domain.MFAKeyField: key}); err != nil {
		log.Printf("ERROR: store the MFA key of project %s: %v", projectID, err)
		return errMFAKeyWrite
	}
	return nil
}

func writeMFAError(w http.ResponseWriter, projectID string, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidMFAKey):
		log.Printf("ERROR: project %s holds an invalid MFA key; MFA was not switched on", projectID)
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, errMFAVaultReadable):
		httpError(w, "the vault is not available", http.StatusServiceUnavailable)
	case errors.Is(err, errMFAKeyWrite):
		httpError(w, err.Error(), http.StatusInternalServerError)
	default:
		log.Printf("ERROR: switch MFA of project %s: %v", projectID, err)
		httpError(w, "failed to save the setting", http.StatusInternalServerError)
	}
}

// authMFAProject validates the project and the wiring; the route's flag gate
// answers 404 while authmfa is off, checked here again to fail closed.
func (h *ProvisioningHandler) authMFAProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID, ok := h.authSettingsRequestProject(w, r)
	if !ok {
		return "", false
	}
	if !h.authMFAOn(r.Context()) {
		http.NotFound(w, r)
		return "", false
	}
	if h.authOAuth.MFA == nil || h.authOAuth.SignIn == nil || h.authOAuth.Secrets == nil {
		httpError(w, "the vault is not available", http.StatusServiceUnavailable)
		return "", false
	}
	return projectID, true
}

// mfaView is the settings' mfaEnabled: present only while authmfa is on.
func (h *ProvisioningHandler) mfaView(ctx context.Context, projectID string) (*bool, error) {
	if !h.authMFAOn(ctx) {
		return nil, nil
	}
	signIn, err := h.storedAuthSignIn(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return &signIn.MFAEnabled, nil
}

// mfaForInfo is /info's mfaEnabled: true only while authmfa is on and the
// project switched MFA on; otherwise the key stays off the wire.
func (h *ProvisioningHandler) mfaForInfo(ctx context.Context, projectID string) (*bool, error) {
	if !h.authMFAOn(ctx) || h.authOAuth.SignIn == nil {
		return nil, nil
	}
	signIn, err := h.authOAuth.SignIn.GetAuthSignIn(ctx, projectID)
	if err != nil || !signIn.MFAEnabled {
		return nil, err
	}
	on := true
	return &on, nil
}
