package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/studiooauth"
)

const (
	auditActionSSOProviderUpdate = "sso_provider.update"
	auditResourceSSOProvider     = "sso_provider"
	maxSSOSecretLength           = 512
)

var ssoClientIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{0,256}$`)

type ssoSecrets interface {
	Get(path string) (map[string]string, error)
	Put(path string, data map[string]string) error
}

// SSOProvidersHandler lets a platform admin turn the Studio sign-in providers
// on and off and set their OAuth client. The client secret is write-only.
type SSOProvidersHandler struct {
	providers []string
	secrets   ssoSecrets
	studioURL string
	audit     auditWriter
}

func NewSSOProvidersHandler(providers []string, secrets ssoSecrets, studioURL string, audit auditWriter) *SSOProvidersHandler {
	return &SSOProvidersHandler{providers: providers, secrets: secrets, studioURL: studioURL, audit: audit}
}

func (h *SSOProvidersHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Put("/{provider}", h.Update)
}

type ssoProviderSettings struct {
	Provider        string `json:"provider"`
	Enabled         bool   `json:"enabled"`
	ClientID        string `json:"clientId"`
	ClientSecretSet bool   `json:"clientSecretSet"`
	CallbackURL     string `json:"callbackUrl"`
}

func (h *SSOProvidersHandler) settings(provider string, stored map[string]string) ssoProviderSettings {
	return ssoProviderSettings{
		Provider:        provider,
		Enabled:         stored[studiooauth.FieldEnabled] == "true",
		ClientID:        stored[studiooauth.FieldClientID],
		ClientSecretSet: stored[studiooauth.FieldClientSecret] != "",
		CallbackURL:     studiooauth.CallbackURL(h.studioURL, provider),
	}
}

// stored reads a provider's settings; a provider never configured has none.
func (h *SSOProvidersHandler) stored(provider string) map[string]string {
	if h.secrets == nil {
		return map[string]string{}
	}
	data, err := h.secrets.Get(studiooauth.SecretPath(provider))
	if err != nil || data == nil {
		return map[string]string{}
	}
	return data
}

func (h *SSOProvidersHandler) List(w http.ResponseWriter, _ *http.Request) {
	out := make([]ssoProviderSettings, 0, len(h.providers))
	for _, provider := range h.providers {
		out = append(out, h.settings(provider, h.stored(provider)))
	}
	writeJSON(w, map[string]any{"providers": out})
}

type ssoProviderUpdate struct {
	Enabled      bool    `json:"enabled"`
	ClientID     string  `json:"clientId"`
	ClientSecret *string `json:"clientSecret"`
}

func (h *SSOProvidersHandler) Update(w http.ResponseWriter, r *http.Request) {
	provider := chi.URLParam(r, "provider")
	if !slices.Contains(h.providers, provider) {
		httpError(w, "unknown sign-in provider", http.StatusNotFound)
		return
	}
	var req ssoProviderUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	next, msg := mergeSSOSettings(h.stored(provider), req)
	if msg != "" {
		httpError(w, msg, http.StatusBadRequest)
		return
	}
	if h.secrets == nil {
		httpError(w, "the vault is not available", http.StatusServiceUnavailable)
		return
	}
	if err := h.secrets.Put(studiooauth.SecretPath(provider), next); err != nil {
		log.Printf("ERROR: save sign-in provider %s: %v", provider, err)
		httpError(w, "failed to save the provider", http.StatusInternalServerError)
		return
	}
	h.record(r, provider, next, req.ClientSecret != nil)
	writeJSON(w, h.settings(provider, next))
}

// mergeSSOSettings applies an update to the stored settings. An omitted
// secret keeps the stored one; enabling needs both a client id and a secret.
func mergeSSOSettings(current map[string]string, req ssoProviderUpdate) (map[string]string, string) {
	if !ssoClientIDPattern.MatchString(req.ClientID) {
		return nil, "clientId has characters a provider never issues"
	}
	secret := current[studiooauth.FieldClientSecret]
	if req.ClientSecret != nil {
		if len(*req.ClientSecret) > maxSSOSecretLength {
			return nil, "clientSecret is too long"
		}
		secret = *req.ClientSecret
	}
	if req.Enabled && (req.ClientID == "" || secret == "") {
		return nil, "enabling a provider needs its client id and client secret"
	}
	enabled := "false"
	if req.Enabled {
		enabled = "true"
	}
	return map[string]string{
		studiooauth.FieldEnabled:      enabled,
		studiooauth.FieldClientID:     req.ClientID,
		studiooauth.FieldClientSecret: secret,
	}, ""
}

func (h *SSOProvidersHandler) record(r *http.Request, provider string, saved map[string]string, secretChanged bool) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{
		"enabled":       saved[studiooauth.FieldEnabled] == "true",
		"clientId":      saved[studiooauth.FieldClientID],
		"secretChanged": secretChanged,
	})
	now := time.Now()
	entry := &domain.AuditEntry{
		Action: auditActionSSOProviderUpdate, Resource: auditResourceSSOProvider, ResourceID: provider,
		Details: string(details), IPAddress: clientIP(r), Timestamp: &now,
	}
	if user := auth.GetUser(r.Context()); user != nil {
		entry.UserID = user.ID
	}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit sign-in provider change %s: %v", provider, err)
	}
}
