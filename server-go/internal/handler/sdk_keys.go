package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sdkkeys"
)

const (
	auditActionSDKKeyCreate = "sdk_key.create"
	auditActionSDKKeyRevoke = "sdk_key.revoke"
	auditResourceSDKKey     = "sdk_key"
	maxSDKKeyNameLength     = 100
)

// SDKKeyManager is the auth service seen from the control plane.
type SDKKeyManager interface {
	List(ctx context.Context, orgSlug, projectID string) ([]sdkkeys.Key, error)
	Create(ctx context.Context, orgSlug, projectID string, req sdkkeys.CreateRequest) (*sdkkeys.CreatedKey, error)
	Revoke(ctx context.Context, orgSlug, projectID string, keyID int64) error
}

type projectLookup interface {
	FindByProjectID(id string) (*domain.DatabaseInstance, error)
}

type orgLookup interface {
	FindOrgByID(ctx context.Context, id string) (*domain.Org, error)
}

// SDKKeysHandler lets a developer generate, list and revoke the project's SDK
// api keys. excalibase-auth mints and stores them; this handler relays the
// Studio user's request with the control plane's own credential.
type SDKKeysHandler struct {
	keys      SDKKeyManager
	instances projectLookup
	orgs      orgLookup
	audit     auditWriter
}

func NewSDKKeysHandler(keys SDKKeyManager, instances projectLookup, orgs orgLookup, audit auditWriter) *SDKKeysHandler {
	return &SDKKeysHandler{keys: keys, instances: instances, orgs: orgs, audit: audit}
}

func (h *SDKKeysHandler) Routes(r chi.Router) {
	r.Get("/", h.List)
	r.Post("/", h.Create)
	r.Delete("/{keyId}", h.Revoke)
}

func (h *SDKKeysHandler) List(w http.ResponseWriter, r *http.Request) {
	projectID, orgSlug, ok := h.resolve(w, r)
	if !ok {
		return
	}
	keys, err := h.keys.List(r.Context(), orgSlug, projectID)
	if err != nil {
		writeSDKKeyError(w, err)
		return
	}
	writeJSON(w, map[string]any{"keys": keys})
}

func (h *SDKKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req sdkkeys.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.KeyType != "publishable" && req.KeyType != "secret" {
		httpError(w, "keyType must be 'publishable' or 'secret'", http.StatusBadRequest)
		return
	}
	if len(req.Name) > maxSDKKeyNameLength {
		httpError(w, "name is too long", http.StatusBadRequest)
		return
	}
	projectID, orgSlug, ok := h.resolve(w, r)
	if !ok {
		return
	}
	created, err := h.keys.Create(r.Context(), orgSlug, projectID, req)
	if err != nil {
		writeSDKKeyError(w, err)
		return
	}
	h.record(r, auditActionSDKKeyCreate, projectID, map[string]any{
		"keyId": created.ID, "keyType": created.KeyType, "keyPrefix": created.KeyPrefix,
	})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(created)
}

func (h *SDKKeysHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	keyID, err := strconv.ParseInt(chi.URLParam(r, "keyId"), 10, 64)
	if err != nil || keyID <= 0 {
		httpError(w, "invalid key id", http.StatusBadRequest)
		return
	}
	projectID, orgSlug, ok := h.resolve(w, r)
	if !ok {
		return
	}
	if err := h.keys.Revoke(r.Context(), orgSlug, projectID, keyID); err != nil {
		writeSDKKeyError(w, err)
		return
	}
	h.record(r, auditActionSDKKeyRevoke, projectID, map[string]any{"keyId": keyID})
	w.WriteHeader(http.StatusNoContent)
}

// resolve names the project and its organisation's slug, which auth routes by.
func (h *SDKKeysHandler) resolve(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if h.keys == nil {
		httpError(w, "SDK keys are unavailable: the auth service is not configured", http.StatusServiceUnavailable)
		return "", "", false
	}
	projectID := chi.URLParam(r, "projectId")
	inst, err := h.instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		httpError(w, "project not found", http.StatusNotFound)
		return "", "", false
	}
	org, err := h.orgs.FindOrgByID(r.Context(), inst.OrgID)
	if err != nil || org == nil || org.Slug == "" {
		log.Printf("ERROR: sdk keys: organisation %q of %s not found: %v", inst.OrgID, projectID, err)
		httpError(w, "the project's organisation could not be resolved", http.StatusInternalServerError)
		return "", "", false
	}
	return projectID, org.Slug, true
}

func (h *SDKKeysHandler) record(r *http.Request, action, projectID string, details map[string]any) {
	if h.audit == nil {
		return
	}
	encoded, _ := json.Marshal(details)
	now := time.Now()
	entry := &domain.AuditEntry{
		Action: action, Resource: auditResourceSDKKey, ResourceID: projectID,
		Details: string(encoded), IPAddress: clientIP(r), Timestamp: &now,
	}
	if user := auth.GetUser(r.Context()); user != nil {
		entry.UserID = user.ID
	}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", action, projectID, err)
	}
}

// writeSDKKeyError maps an auth failure. A refusal of our own credential is
// the platform's fault (502), not the caller's.
func writeSDKKeyError(w http.ResponseWriter, err error) {
	var refused *sdkkeys.RefusedError
	switch {
	case errors.Is(err, sdkkeys.ErrAuthUnavailable):
		httpError(w, "the auth service is unavailable; try again", http.StatusServiceUnavailable)
	case errors.As(err, &refused) && refused.Status == http.StatusBadRequest:
		httpError(w, refused.Message, http.StatusBadRequest)
	case errors.As(err, &refused):
		log.Printf("ERROR: auth refused an sdk key request: %v", err)
		httpError(w, "the auth service refused the request", http.StatusBadGateway)
	default:
		log.Printf("ERROR: sdk keys: %v", err)
		httpError(w, "failed to manage SDK keys", http.StatusInternalServerError)
	}
}
