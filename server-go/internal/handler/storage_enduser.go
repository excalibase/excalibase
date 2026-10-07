package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// EndUserVerifier checks an app user's access token for a project.
type EndUserVerifier interface {
	VerifyEndUser(token, projectID string) (jwt.MapClaims, error)
}

// EndUserStorageHandler serves a project's buckets to its app users at
// /storage/v1/{projectId}/buckets/{bucket}/... (EXC-560). The caller is the
// app user of a verified project token; what they may do is the bucket's
// access rule for their role. Bytes still go straight to the object store
// through signed URLs.
type EndUserStorageHandler struct {
	storage  *StorageHandler
	verifier EndUserVerifier
	cors     storage.ProjectCorsStore
}

func NewEndUserStorageHandler(storageHandler *StorageHandler, verifier EndUserVerifier, cors storage.ProjectCorsStore) *EndUserStorageHandler {
	return &EndUserStorageHandler{storage: storageHandler, verifier: verifier, cors: cors}
}

const (
	endUserCORSMethods     = "GET, POST, DELETE, OPTIONS"
	errStorageNotAllowed   = "not allowed"
	maxEndUserStorageBody  = 64 << 10
	endUserOwnerPrefix     = "end-user:"
	roleHeader             = "X-Excalibase-Role"
	errStorageUnauthorized = "missing or invalid Authorization header"
)

// Routes mounts under /storage/v1/{projectId}.
func (h *EndUserStorageHandler) Routes(r chi.Router) {
	r.Use(h.corsMiddleware)
	r.Post("/buckets/{bucket}/upload-url", h.signUpload)
	r.Post("/buckets/{bucket}/confirm-upload", h.confirmUpload)
	r.Get("/buckets/{bucket}/objects", h.listObjects)
	r.Get("/buckets/{bucket}/download-url/*", h.signDownload)
	r.Delete("/buckets/{bucket}/objects/*", h.deleteObject)
}

// corsMiddleware grants the project's allowlisted origins and answers
// preflights itself.
func (h *EndUserStorageHandler) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allowed := projectCorsGrant(r, chi.URLParam(r, "projectId"), h.cors); allowed != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", endUserCORSMethods)
				w.Header().Set("Access-Control-Allow-Headers", functionCORSHeaders)
				w.Header().Set("Access-Control-Max-Age", "3600")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// endUserObject is an object as an app user sees it: no uploader identity.
type endUserObject struct {
	Key       string    `json:"key"`
	Size      int64     `json:"size"`
	MimeType  string    `json:"mimeType"`
	ETag      string    `json:"etag,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toEndUserObject(o storagesvc.Object) endUserObject {
	return endUserObject{Key: o.Key, Size: o.Size, MimeType: o.MimeType, ETag: o.ETag, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt}
}

// caller verifies the bearer token and picks the role it acts as: the
// token's role, or one of its allowed_roles named in X-Excalibase-Role.
func (h *EndUserStorageHandler) caller(w http.ResponseWriter, r *http.Request, projectID string) (storagesvc.EndUser, bool) {
	header := r.Header.Get("Authorization")
	token, found := strings.CutPrefix(header, "Bearer ")
	if !found || token == "" || h.verifier == nil {
		httpError(w, errStorageUnauthorized, http.StatusUnauthorized)
		return storagesvc.EndUser{}, false
	}
	claims, err := h.verifier.VerifyEndUser(token, projectID)
	if err != nil {
		var coded *codedJWTError
		if errors.As(err, &coded) {
			httpError(w, coded.Code(), http.StatusUnauthorized)
			return storagesvc.EndUser{}, false
		}
		if isVaultUnavailableError(err) {
			httpError(w, "auth temporarily unavailable", http.StatusServiceUnavailable)
			return storagesvc.EndUser{}, false
		}
		httpError(w, "invalid jwt: "+safeError(err), http.StatusUnauthorized)
		return storagesvc.EndUser{}, false
	}
	role, _ := claims["role"].(string)
	if requested := r.Header.Get(roleHeader); requested != "" && requested != role {
		if !claimListsRole(claims, requested) {
			httpError(w, errStorageNotAllowed, http.StatusForbidden)
			return storagesvc.EndUser{}, false
		}
		role = requested
	}
	return storagesvc.EndUser{Subject: userIDClaim(claims), Role: role}, true
}

// userIDClaim is the signed-in user's id, which names their own folder. An
// api-key token has none: every visitor holding the publishable key shares
// it, so it owns nothing.
func userIDClaim(claims jwt.MapClaims) string {
	switch id := claims["userId"].(type) {
	case float64:
		if id > 0 && id == float64(int64(id)) {
			return strconv.FormatInt(int64(id), 10)
		}
	case json.Number:
		if n, err := id.Int64(); err == nil && n > 0 {
			return strconv.FormatInt(n, 10)
		}
	}
	return ""
}

func claimListsRole(claims jwt.MapClaims, role string) bool {
	listed, _ := claims["allowed_roles"].([]any)
	for _, entry := range listed {
		if name, ok := entry.(string); ok && name == role {
			return true
		}
	}
	return false
}

// authorize resolves the caller and the bucket and applies the bucket's rule
// for op on key. An unknown bucket is refused exactly like a missing rule, so
// app users cannot probe which buckets exist.
func (h *EndUserStorageHandler) authorize(w http.ResponseWriter, r *http.Request, op storagesvc.Operation, key string) (storagesvc.EndUser, bool) {
	projectID := chi.URLParam(r, "projectId")
	user, ok := h.caller(w, r, projectID)
	if !ok {
		return user, false
	}
	bucket, err := h.storage.svc.Bucket(r.Context(), projectID, chi.URLParam(r, "bucket"))
	if errors.Is(err, storagesvc.ErrBucketNotFound) {
		httpError(w, errStorageNotAllowed, http.StatusForbidden)
		return user, false
	}
	if err != nil {
		storageError(w, "end-user bucket lookup", err)
		return user, false
	}
	if !bucket.Allows(user, op, key) {
		httpError(w, errStorageNotAllowed, http.StatusForbidden)
		return user, false
	}
	return user, true
}

func decodeEndUserBody(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxEndUserStorageBody)).Decode(into); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return false
	}
	return true
}

func (h *EndUserStorageHandler) signUpload(w http.ResponseWriter, r *http.Request) {
	var req storagesvc.UploadURLRequest
	if !decodeEndUserBody(w, r, &req) {
		return
	}
	if _, ok := h.authorize(w, r, storagesvc.OpWrite, req.Key); !ok {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	tier, err := h.storage.tierFor(projectID)
	if err != nil {
		storageError(w, "read project tier", err)
		return
	}
	out, err := h.storage.svc.SignUploadURL(r.Context(), projectID, chi.URLParam(r, "bucket"), tier, req)
	if err != nil {
		storageError(w, "sign upload url", err)
		return
	}
	writeJSON(w, out)
}

func (h *EndUserStorageHandler) confirmUpload(w http.ResponseWriter, r *http.Request) {
	var req storagesvc.ConfirmUploadRequest
	if !decodeEndUserBody(w, r, &req) {
		return
	}
	user, ok := h.authorize(w, r, storagesvc.OpWrite, req.Key)
	if !ok {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	tier, err := h.storage.tierFor(projectID)
	if err != nil {
		storageError(w, "read project tier", err)
		return
	}
	obj, err := h.storage.svc.ConfirmUpload(r.Context(), projectID, chi.URLParam(r, "bucket"), tier, endUserOwnerPrefix+user.Subject, req)
	if err != nil {
		storageError(w, "confirm upload", err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, toEndUserObject(*obj))
}

func (h *EndUserStorageHandler) listObjects(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	user, ok := h.caller(w, r, projectID)
	if !ok {
		return
	}
	bucket, err := h.storage.svc.Bucket(r.Context(), projectID, chi.URLParam(r, "bucket"))
	if errors.Is(err, storagesvc.ErrBucketNotFound) {
		httpError(w, errStorageNotAllowed, http.StatusForbidden)
		return
	}
	if err != nil {
		storageError(w, "end-user bucket lookup", err)
		return
	}
	query := r.URL.Query()
	prefix, allowed := bucket.ListPrefix(user, query.Get("prefix"))
	if !allowed {
		httpError(w, errStorageNotAllowed, http.StatusForbidden)
		return
	}
	limit := 100
	if raw := query.Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil {
			httpError(w, "limit must be an integer", http.StatusBadRequest)
			return
		}
	}
	page, err := h.storage.svc.ListObjects(r.Context(), projectID, bucket.Name, storagesvc.ListObjectsRequest{
		Prefix: prefix, Cursor: query.Get("cursor"), Limit: limit,
	})
	if err != nil {
		storageError(w, "list objects", err)
		return
	}
	objects := make([]endUserObject, 0, len(page.Objects))
	for _, o := range page.Objects {
		objects = append(objects, toEndUserObject(o))
	}
	writeJSON(w, map[string]any{"objects": objects, "nextCursor": page.NextCursor})
}

func (h *EndUserStorageHandler) signDownload(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if _, ok := h.authorize(w, r, storagesvc.OpRead, key); !ok {
		return
	}
	out, err := h.storage.svc.SignDownloadURL(r.Context(), chi.URLParam(r, "projectId"), chi.URLParam(r, "bucket"), key)
	if err != nil {
		storageError(w, "sign download url", err)
		return
	}
	writeJSON(w, out)
}

func (h *EndUserStorageHandler) deleteObject(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if _, ok := h.authorize(w, r, storagesvc.OpDelete, key); !ok {
		return
	}
	if err := h.storage.svc.DeleteObject(r.Context(), chi.URLParam(r, "projectId"), chi.URLParam(r, "bucket"), key); err != nil {
		storageError(w, "delete object", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
