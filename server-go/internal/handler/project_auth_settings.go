package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

// errAuthSettingsNotConfigured is returned when no ProjectAuthSettingsStore
// is wired — the API fails closed rather than pretending a setting was saved.
const errAuthSettingsNotConfigured = "project auth settings not configured"

// authSettingsResponse is the wire shape of GET/PUT
// /api/projects/{id}/auth-settings.
type authSettingsResponse struct {
	RequireEmailVerification bool   `json:"requireEmailVerification"`
	SiteURL                  string `json:"siteUrl"`
	// Present only while authoauth or authotp is on (EXC-662).
	*authSignInResponse
}

type authSignInResponse struct {
	RedirectURLs  []string `json:"redirectUrls"`
	AllowNewUsers bool     `json:"allowNewUsers"`
	// Present only while authotp is on.
	EmailOTPEnabled *bool `json:"emailOtpEnabled,omitempty"`
}

// authSettingsRequest is the PUT body. The sign-in fields are read only while
// their flag is on, and an absent one keeps its stored value so an older
// Studio cannot wipe them.
type authSettingsRequest struct {
	RequireEmailVerification bool      `json:"requireEmailVerification"`
	SiteURL                  string    `json:"siteUrl"`
	RedirectURLs             *[]string `json:"redirectUrls"`
	AllowNewUsers            *bool     `json:"allowNewUsers"`
	EmailOTPEnabled          *bool     `json:"emailOtpEnabled"`
}

// GetAuthSettings serves GET /api/projects/{projectId}/auth-settings.
func (h *ProvisioningHandler) GetAuthSettings(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authSettingsRequestProject(w, r)
	if !ok {
		return
	}
	settings, _, err := h.authSettingsStore.GetAuthSettings(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	response := authSettingsResponseFor(settings)
	if h.signInPolicyOn(r.Context()) {
		signIn, err := h.storedAuthSignIn(r.Context(), projectID)
		if err != nil {
			httpError(w, safeError(err), http.StatusInternalServerError)
			return
		}
		response.authSignInResponse = h.authSignInResponseFor(r.Context(), signIn)
	}
	writeJSON(w, response)
}

// PutAuthSettings serves PUT /api/projects/{projectId}/auth-settings with
// body {"requireEmailVerification": bool, "siteUrl": string}. The site URL is
// validated and canonicalised before anything is written. The auth service
// picks up the change on its next /info refresh (TTL, not push).
func (h *ProvisioningHandler) PutAuthSettings(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.authSettingsRequestProject(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var body authSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpError(w, errInvalidRequestBody, http.StatusBadRequest)
		return
	}
	siteURL, err := domain.ValidateSiteURL(body.SiteURL)
	if err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	if body.RequireEmailVerification && siteURL == "" {
		httpError(w, domain.ErrSiteURLRequired.Error(), http.StatusBadRequest)
		return
	}
	settings := domain.ProjectAuthSettings{
		RequireEmailVerification: body.RequireEmailVerification,
		SiteURL:                  siteURL,
	}
	if h.signInPolicyOn(r.Context()) {
		h.putAuthSettingsWithSignIn(w, r, projectID, settings, body)
		return
	}
	if err := h.authSettingsStore.SetAuthSettings(r.Context(), projectID, settings); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, authSettingsResponseFor(settings))
}

// authSettingsRequestProject validates the path project and the store wiring
// shared by both verbs, writing the error response itself when either fails.
func (h *ProvisioningHandler) authSettingsRequestProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	projectID := chi.URLParam(r, "projectId")
	if !isValidID(projectID) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	if h.authSettingsStore == nil {
		httpError(w, errAuthSettingsNotConfigured, http.StatusServiceUnavailable)
		return "", false
	}
	return projectID, true
}

// authSettingsForInfo is the settings exposed on /info. A missing store reads
// as the zero value (verification off, no site URL); a store failure is
// surfaced so the caller keeps its last good copy instead of caching a wrong
// default.
func (h *ProvisioningHandler) authSettingsForInfo(ctx context.Context, projectID string) (domain.ProjectAuthSettings, error) {
	if h.authSettingsStore == nil {
		return domain.ProjectAuthSettings{}, nil
	}
	settings, _, err := h.authSettingsStore.GetAuthSettings(ctx, projectID)
	return settings, err
}

// putAuthSettingsWithSignIn is the sign-in-flag-on write: the settings and the
// sign-in policy in one upsert, absent policy fields keeping their stored value.
func (h *ProvisioningHandler) putAuthSettingsWithSignIn(w http.ResponseWriter, r *http.Request, projectID string, settings domain.ProjectAuthSettings, body authSettingsRequest) {
	var redirectURLs []string
	if body.RedirectURLs != nil {
		normalized, err := domain.NormalizeRedirectURLs(*body.RedirectURLs)
		if err != nil {
			httpError(w, safeError(err), http.StatusBadRequest)
			return
		}
		redirectURLs = normalized
	}
	signIn, err := h.storedAuthSignIn(r.Context(), projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	if body.RedirectURLs != nil {
		signIn.RedirectURLs = redirectURLs
	}
	if body.AllowNewUsers != nil {
		signIn.AllowNewUsers = *body.AllowNewUsers
	}
	if body.EmailOTPEnabled != nil && h.authOTPOn(r.Context()) {
		signIn.EmailOTPEnabled = *body.EmailOTPEnabled
	}
	if err := h.authOAuth.SignIn.SetAuthSettingsWithSignIn(r.Context(), projectID, settings, signIn); err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return
	}
	h.recordAuthSignIn(r, projectID, settings, signIn)
	response := authSettingsResponseFor(settings)
	response.authSignInResponse = h.authSignInResponseFor(r.Context(), signIn)
	writeJSON(w, response)
}

// storedAuthSignIn reads the sign-in policy; without its store there is none to report.
func (h *ProvisioningHandler) storedAuthSignIn(ctx context.Context, projectID string) (domain.ProjectAuthSignIn, error) {
	if h.authOAuth.SignIn == nil {
		return domain.ProjectAuthSignIn{}, errors.New(errAuthSettingsNotConfigured)
	}
	return h.authOAuth.SignIn.GetAuthSignIn(ctx, projectID)
}

// authOAuthForInfo is the sign-in policy on /info: nil while both sign-in
// flags are off, so none of its keys appear; providers only with authoauth,
// the email switch only with authotp. A read failure is returned, never defaulted.
func (h *ProvisioningHandler) authOAuthForInfo(ctx context.Context, projectID, orgSlug string) (*domain.ProjectOAuthInfo, error) {
	if !h.signInPolicyOn(ctx) {
		return nil, nil
	}
	info := &domain.ProjectOAuthInfo{RedirectURLs: []string{}, AllowNewUsers: true, OAuthProviders: []domain.OAuthProviderInfo{}}
	if h.authOAuth.SignIn != nil {
		signIn, err := h.authOAuth.SignIn.GetAuthSignIn(ctx, projectID)
		if err != nil {
			return nil, err
		}
		info.AllowNewUsers = signIn.AllowNewUsers
		if signIn.RedirectURLs != nil {
			info.RedirectURLs = signIn.RedirectURLs
		}
		if h.authOTPOn(ctx) {
			info.EmailOTPEnabled = &signIn.EmailOTPEnabled
		}
	}
	if h.authOAuth.Providers != nil && h.authOAuthOn(ctx) {
		stored, err := h.authOAuth.Providers.ListAuthProviders(ctx, projectID)
		if err != nil {
			return nil, err
		}
		info.OAuthProviders = domain.UsableOAuthProviders(stored, h.authOAuth.PublicBaseURL, orgSlug, projectID)
	}
	return info, nil
}

func (h *ProvisioningHandler) authSignInResponseFor(ctx context.Context, signIn domain.ProjectAuthSignIn) *authSignInResponse {
	urls := signIn.RedirectURLs
	if urls == nil {
		urls = []string{}
	}
	response := &authSignInResponse{RedirectURLs: urls, AllowNewUsers: signIn.AllowNewUsers}
	if h.authOTPOn(ctx) {
		response.EmailOTPEnabled = &signIn.EmailOTPEnabled
	}
	return response
}

func authSettingsResponseFor(settings domain.ProjectAuthSettings) authSettingsResponse {
	return authSettingsResponse{
		RequireEmailVerification: settings.RequireEmailVerification,
		SiteURL:                  settings.SiteURL,
	}
}

// recordAuthSignIn audits the sign-in policy: the redirect list decides where
// sign-in codes are delivered.
func (h *ProvisioningHandler) recordAuthSignIn(r *http.Request, projectID string, settings domain.ProjectAuthSettings, signIn domain.ProjectAuthSignIn) {
	fields := map[string]any{
		"siteUrl": settings.SiteURL, "redirectUrls": signIn.RedirectURLs, "allowNewUsers": signIn.AllowNewUsers,
	}
	if h.authOTPOn(r.Context()) {
		fields["emailOtpEnabled"] = signIn.EmailOTPEnabled
	}
	details, _ := json.Marshal(fields)
	h.writeAuthAudit(r, projectID, auditActionAuthSettingsUpdate, auditResourceAuthSettings, projectID, string(details))
}
