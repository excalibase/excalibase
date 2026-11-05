package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/s3keys"
	"github.com/go-chi/chi/v5"
)

// S3 access keys (EXC-630, ADR 0044): made, listed and revoked here, used at
// the S3 gateway. The secret is in the create answer only.

// S3Region is the region clients are told to sign with. The gateway accepts
// any region, so this only has to be one every SDK knows.
const S3Region = "us-east-1"

const maxS3KeyBodyBytes = 16 << 10

type s3KeyService interface {
	Create(ctx context.Context, request s3keys.CreateRequest) (string, *s3keys.Key, error)
	List(ctx context.Context, projectID string) ([]*s3keys.Key, error)
	Revoke(ctx context.Context, projectID, id string) (*s3keys.Key, error)
}

// S3KeysHandler serves /api/projects/{projectId}/storage/s3-keys.
type S3KeysHandler struct {
	keys     s3KeyService
	endpoint string
	audit    auditWriter
}

// NewS3KeysHandler wires the key service; endpoint is the gateway's public URL.
func NewS3KeysHandler(keys s3KeyService, endpoint string) *S3KeysHandler {
	return &S3KeysHandler{keys: keys, endpoint: endpoint}
}

// SetAuditLog records keys made and revoked; nil records nothing.
func (h *S3KeysHandler) SetAuditLog(audit auditWriter) { h.audit = audit }

// Routes mounts under the project's storage route, which carries project
// access and the Developer+ rung for writes.
func (h *S3KeysHandler) Routes(r chi.Router) {
	r.Get("/s3-keys", h.list)
	r.Post("/s3-keys", h.create)
	r.Delete("/s3-keys/{keyId}", h.revoke)
}

type s3KeyRequest struct {
	Name          string             `json:"name"`
	Buckets       []string           `json:"buckets"`
	Permissions   s3keys.Permissions `json:"permissions"`
	ExpiresInDays *int               `json:"expiresInDays,omitempty"`
}

type s3KeyListResponse struct {
	Keys     []*s3keys.Key `json:"keys"`
	Endpoint string        `json:"endpoint"`
	Region   string        `json:"region"`
}

type s3KeyCreatedResponse struct {
	*s3keys.Key
	SecretAccessKey string `json:"secretAccessKey"`
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
}

func (h *S3KeysHandler) list(w http.ResponseWriter, r *http.Request) {
	keys, err := h.keys.List(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		writeS3KeyError(w, "list", err)
		return
	}
	writeJSON(w, s3KeyListResponse{Keys: keys, Endpoint: h.endpoint, Region: S3Region})
}

func (h *S3KeysHandler) create(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxS3KeyBodyBytes))
	decoder.DisallowUnknownFields()
	var body s3KeyRequest
	if err := decoder.Decode(&body); err != nil {
		httpError(w, errInvalidJSON, http.StatusBadRequest)
		return
	}
	request, err := s3KeyRequestFor(r, body)
	if err != nil {
		writeS3KeyError(w, "create", err)
		return
	}
	secret, key, err := h.keys.Create(r.Context(), request)
	if err != nil {
		writeS3KeyError(w, "create", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.auditKey(r, "storage_s3_key.create", key)
	writeJSONStatus(w, http.StatusCreated, s3KeyCreatedResponse{Key: key, SecretAccessKey: secret, Endpoint: h.endpoint, Region: S3Region})
}

// s3KeyRequestFor turns the body into a request, capping the expiry at the
// caller's own token unless it is a Studio session, which is renewed by login.
func s3KeyRequestFor(r *http.Request, body s3KeyRequest) (s3keys.CreateRequest, error) {
	request := s3keys.CreateRequest{
		ProjectID: chi.URLParam(r, "projectId"), Name: body.Name, Actor: actorID(r),
		Buckets: body.Buckets, Permissions: body.Permissions,
	}
	if body.ExpiresInDays != nil {
		days := *body.ExpiresInDays
		if days < 1 || days > int(s3keys.MaxLifetime/(24*time.Hour)) {
			return request, &s3keys.InputError{Reason: "expiresInDays must be between 1 and 365"}
		}
		request.Lifetime = time.Duration(days) * 24 * time.Hour
	}
	if caller := auth.GetToken(r.Context()); caller != nil && !auth.IsSessionToken(caller) {
		request.NotAfter = caller.ExpiresAt
	}
	return request, nil
}

func (h *S3KeysHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "keyId")
	if !isValidID(id) {
		httpError(w, "invalid keyId", http.StatusBadRequest)
		return
	}
	key, err := h.keys.Revoke(r.Context(), chi.URLParam(r, "projectId"), id)
	if err != nil {
		writeS3KeyError(w, "revoke", err)
		return
	}
	h.auditKey(r, "storage_s3_key.revoke", key)
	w.WriteHeader(http.StatusNoContent)
}

func writeS3KeyError(w http.ResponseWriter, op string, err error) {
	var input *s3keys.InputError
	switch {
	case errors.As(err, &input):
		httpError(w, input.Error(), http.StatusBadRequest)
	case errors.Is(err, s3keys.ErrNotFound):
		httpError(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, s3keys.ErrLimitReached):
		httpError(w, err.Error(), http.StatusConflict)
	default:
		log.Printf("s3 keys %s: %v", op, err)
		httpError(w, "the S3 access keys could not be changed or read", http.StatusInternalServerError)
	}
}

// auditKey records a key made or revoked by its access key id, never its secret.
func (h *S3KeysHandler) auditKey(r *http.Request, action string, key *s3keys.Key) {
	if h.audit == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{"accessKeyId": key.AccessKeyID, "name": key.Name,
		"buckets": key.Buckets, "permissions": key.Permissions})
	now := time.Now()
	entry := &domain.AuditEntry{
		Action: action, Resource: "storage_s3_key", ResourceID: key.ID, ProjectID: chi.URLParam(r, "projectId"),
		Details: string(details), IPAddress: clientIP(r), Timestamp: &now, UserID: actorID(r),
	}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", action, key.ID, err)
	}
}
