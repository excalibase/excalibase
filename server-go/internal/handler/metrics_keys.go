package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/promexport"
)

// Metrics keys (EXC-657): a personal access token holding only the
// metrics:read scope, bound to one project, made and revoked by its Admins.
// middleware.MetricsKeyGate refuses it on every route but that project's
// Prometheus endpoint. The raw key is in the create answer only.

const (
	maxMetricsKeysPerProject = 10
	maxMetricsKeyNameLength  = 64
	defaultMetricsKeyDays    = 90
	maxMetricsKeyDays        = 365
	maxMetricsKeyBodyBytes   = 4 << 10
	errMetricsKeyNotFound    = "metrics key not found"
)

// MetricsKeyStore is the access_tokens table, as metrics keys use it.
type MetricsKeyStore interface {
	CreateToken(ctx context.Context, token *domain.AccessToken) error
	FindByTokenHash(ctx context.Context, hash string) (*domain.AccessToken, error)
	ListTokensByProject(ctx context.Context, projectID string) ([]*domain.AccessToken, error)
	DeleteToken(ctx context.Context, tokenHash string) error
}

type MetricsKeysHandler struct {
	tokens MetricsKeyStore
	audit  auditWriter
}

// NewMetricsKeysHandler takes an optional audit log; nil records nothing.
func NewMetricsKeysHandler(tokens MetricsKeyStore, audit auditWriter) *MetricsKeysHandler {
	return &MetricsKeysHandler{tokens: tokens, audit: audit}
}

// Routes mounts the key routes; createLimit bounds minting only.
func (h *MetricsKeysHandler) Routes(createLimit func(http.Handler) http.Handler) func(chi.Router) {
	return func(r chi.Router) {
		r.Get("/", h.list)
		r.With(createLimit).Post("/", h.create)
		r.Delete("/{keyId}", h.revoke)
	}
}

// metricsKeyView is a key as Studio lists it. ID is the token's hash, the
// identifier revoke takes, never the key itself.
type metricsKeyView struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	TokenPrefix string     `json:"tokenPrefix"`
	CreatedAt   *time.Time `json:"createdAt,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	LastUsed    *time.Time `json:"lastUsed,omitempty"`
}

type issuedMetricsKey struct {
	metricsKeyView
	Token string `json:"token"`
}

func viewOf(token *domain.AccessToken) metricsKeyView {
	return metricsKeyView{ID: token.TokenHash, Name: token.Name, TokenPrefix: token.TokenPrefix,
		CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt, LastUsed: token.LastUsed}
}

// projectKeys are projectID's metrics keys, whoever made them.
func (h *MetricsKeysHandler) projectKeys(ctx context.Context, projectID string) ([]*domain.AccessToken, error) {
	tokens, err := h.tokens.ListTokensByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	keys := make([]*domain.AccessToken, 0, len(tokens))
	for _, token := range tokens {
		if auth.IsMetricsKey(token) && token.ProjectID == projectID {
			keys = append(keys, token)
		}
	}
	return keys, nil
}

func (h *MetricsKeysHandler) list(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	keys, err := h.projectKeys(r.Context(), projectID)
	if err != nil {
		log.Printf("metrics keys of %s: %v", safeLog(projectID), err)
		httpError(w, "metrics keys could not be read", http.StatusInternalServerError)
		return
	}
	views := make([]metricsKeyView, 0, len(keys))
	for _, key := range keys {
		views = append(views, viewOf(key))
	}
	writeJSON(w, views)
}

type metricsKeyRequest struct {
	Name          string `json:"name"`
	ExpiresInDays *int   `json:"expiresInDays,omitempty"`
}

func validMetricsKeyName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || utf8.RuneCountInString(name) > maxMetricsKeyNameLength {
		return false
	}
	return !strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

// metricsKeyExpiry is now plus the asked lifetime, never past the token the
// caller made it with unless that is a Studio session, which login renews.
func metricsKeyExpiry(r *http.Request, body metricsKeyRequest, now time.Time) (time.Time, error) {
	days := defaultMetricsKeyDays
	if body.ExpiresInDays != nil {
		days = *body.ExpiresInDays
	}
	if days < 1 || days > maxMetricsKeyDays {
		return time.Time{}, errors.New("expiresInDays must be between 1 and 365")
	}
	expires := now.Add(time.Duration(days) * 24 * time.Hour)
	if caller := auth.GetToken(r.Context()); caller != nil && !auth.IsSessionToken(caller) && caller.ExpiresAt != nil && caller.ExpiresAt.Before(expires) {
		expires = *caller.ExpiresAt
	}
	if !expires.After(now) {
		return time.Time{}, errors.New("the token making this metrics key has expired")
	}
	return expires, nil
}

func (h *MetricsKeysHandler) create(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var body metricsKeyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMetricsKeyBodyBytes)).Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	if !validMetricsKeyName(body.Name) {
		httpError(w, "a metrics key name is 1 to 64 characters, without control characters", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC()
	expires, err := metricsKeyExpiry(r, body, now)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	existing, err := h.projectKeys(r.Context(), projectID)
	if err != nil {
		log.Printf("metrics keys of %s: %v", safeLog(projectID), err)
		httpError(w, "the metrics key could not be made", http.StatusInternalServerError)
		return
	}
	if len(existing) >= maxMetricsKeysPerProject {
		httpError(w, "the project already holds as many metrics keys as it may; revoke one first", http.StatusConflict)
		return
	}
	raw := auth.GenerateToken()
	key := &domain.AccessToken{
		TokenHash: auth.HashToken(raw), TokenPrefix: auth.TokenPrefix(raw), UserID: actorID(r),
		Name: strings.TrimSpace(body.Name), Scopes: auth.ScopeMetricsRead, ProjectID: projectID,
		CreatedAt: &now, ExpiresAt: &expires,
	}
	if err := h.tokens.CreateToken(r.Context(), key); err != nil {
		log.Printf("metrics key for %s: %v", safeLog(projectID), err)
		httpError(w, "the metrics key could not be made", http.StatusInternalServerError)
		return
	}
	h.record(r, "metrics_key.create", key)
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, http.StatusCreated, issuedMetricsKey{metricsKeyView: viewOf(key), Token: raw})
}

func (h *MetricsKeysHandler) revoke(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	key, err := h.tokens.FindByTokenHash(r.Context(), chi.URLParam(r, "keyId"))
	if err != nil || key == nil || !auth.IsMetricsKey(key) || key.ProjectID != projectID {
		httpError(w, errMetricsKeyNotFound, http.StatusNotFound)
		return
	}
	if err := h.tokens.DeleteToken(r.Context(), key.TokenHash); err != nil {
		log.Printf("revoke metrics key of %s: %v", safeLog(projectID), err)
		httpError(w, "the metrics key could not be revoked", http.StatusInternalServerError)
		return
	}
	h.record(r, "metrics_key.revoke", key)
	w.WriteHeader(http.StatusNoContent)
}

// record audits a key made or revoked, by its prefix only.
func (h *MetricsKeysHandler) record(r *http.Request, action string, key *domain.AccessToken) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]string{"name": key.Name, "tokenPrefix": key.TokenPrefix})
	now := time.Now()
	entry := &domain.AuditEntry{
		UserID: actorID(r), Action: action, Resource: "metrics_key", ResourceID: key.TokenPrefix,
		ProjectID: key.ProjectID, Details: string(details), IPAddress: clientIP(r), Timestamp: &now,
	}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", action, key.TokenPrefix, err)
	}
}

// MetricsSnapshotter reads what one project's endpoint answers.
type MetricsSnapshotter interface {
	Snapshot(ctx context.Context, projectID string) (promexport.Snapshot, error)
}

// MetricsEndpointHandler serves a project's stored metrics in the Prometheus
// text format, for its own Grafana or Datadog to scrape (EXC-657).
type MetricsEndpointHandler struct {
	source MetricsSnapshotter
}

func NewMetricsEndpointHandler(source MetricsSnapshotter) *MetricsEndpointHandler {
	return &MetricsEndpointHandler{source: source}
}

func (h *MetricsEndpointHandler) Serve(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	snapshot, err := h.source.Snapshot(r.Context(), projectID)
	var body bytes.Buffer
	if err == nil {
		err = promexport.Encode(&body, snapshot)
	}
	if err != nil {
		log.Printf("metrics endpoint of %s: %v", safeLog(projectID), err)
		httpError(w, "metrics could not be read", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", promexport.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body.Bytes())
}
