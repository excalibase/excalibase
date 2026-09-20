package handler

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// StorageHandler exposes Supabase-style storage endpoints under
// /api/projects/{projectId}/storage. Bucket lifecycle, signed-URL
// minting, list/delete, and a public fast-path for anonymous reads.
//
// Auth model:
//   - All /api/projects/.../storage routes require an authenticated user
//     with project access (RequireProjectAccess middleware applied at the
//     parent route in main.go).
//   - The public path (/storage/v1/object/public/{bucket}/{key}) is
//     mounted separately, OUTSIDE /api, with no auth — buckets marked
//     public are served straight through.
//   - The internal path (/internal/storage/{projectId}/...) is mounted by
//     InternalRoutes and authenticates with the runtime-token shared
//     secret. Used by the Deno runtime to fulfil `ctx.storage` calls
//     without ever holding user credentials.
type StorageHandler struct {
	svc           *storagesvc.Service
	store         storage.InstanceStore // looks up project tier for quota enforcement
	runtimeSecret string                // shared secret for /internal/storage/* (Phase 10)
	tusd          *tusd.Handler         // resumable/multipart uploads; nil = feature disabled
}

func NewStorageHandler(svc *storagesvc.Service, store storage.InstanceStore) *StorageHandler {
	return &StorageHandler{svc: svc, store: store}
}

// SetRuntimeSecret wires the shared secret used by the Deno runtime when
// it calls into the storage internal routes. Pass the same value the
// runtime starts with (RUNTIME_SECRET). An empty string disables the
// internal routes (all calls 401).
func (h *StorageHandler) SetRuntimeSecret(secret string) {
	h.runtimeSecret = secret
}

// ctxStorageBucket is the per-project conventional bucket name used to
// back `ctx.storage`. Convex models all uploaded blobs as members of a
// virtual `_storage` table; we materialise that as a single private
// bucket per project, auto-provisioned on first use. Underscores aren't
// legal S3/R2 bucket names so the wire name uses a hyphen — the worker-
// side branding still ties Id<"_storage"> to this bucket.
const ctxStorageBucket = "ctx-storage"

const (
	errObjectNotFound    = "object not found"
	errStorageIDRequired = "storageId required"
	// errStorageFailed is the single message every internal storage failure
	// gets. The real cause is logged server-side; the caller learns only
	// that the operation did not happen.
	errStorageFailed = "storage operation failed"
)

// storageError maps a storage-service error onto a response. Sentinels and
// validation errors carry text written for the caller; anything else is an
// internal fault, logged in full and answered with a fixed message so no
// platform detail reaches the client.
func storageError(w http.ResponseWriter, op string, err error) {
	var invalid *storagesvc.ValidationError
	switch {
	case errors.Is(err, storagesvc.ErrBucketNotFound), errors.Is(err, storagesvc.ErrObjectNotFound):
		httpError(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, storagesvc.ErrBucketExists):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, storagesvc.ErrBucketDeleting):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.As(err, &invalid):
		httpError(w, invalid.Error(), http.StatusBadRequest)
	default:
		log.Printf("storage %s: %v", op, err)
		httpError(w, errStorageFailed, http.StatusInternalServerError)
	}
}

// Routes mounts the project-scoped storage endpoints. Caller is expected
// to apply auth + RequireProjectAccess middleware at the parent.
func (h *StorageHandler) Routes(r chi.Router) {
	r.Get("/buckets", h.ListBuckets)
	r.Post("/buckets", h.CreateBucket)
	r.Delete("/buckets/{bucket}", h.DeleteBucket)
	r.Get("/buckets/{bucket}/objects", h.ListObjects)
	r.Post("/buckets/{bucket}/upload-url", h.SignUploadURL)
	r.Post("/buckets/{bucket}/confirm-upload", h.ConfirmUpload)
	r.Get("/buckets/{bucket}/objects/*", h.GetObjectMetadata)
	r.Get("/buckets/{bucket}/download-url/*", h.SignDownloadURL)
	r.Delete("/buckets/{bucket}/objects/*", h.DeleteObject)
	// Resumable/multipart uploads (tus) — only when enabled via
	// EnableResumableUploads. Both the collection root (POST create) and the
	// per-upload resource (HEAD/PATCH/DELETE) route through serveTus.
	if h.tusd != nil {
		r.Handle("/tus", http.HandlerFunc(h.serveTus))
		r.Handle("/tus/*", http.HandlerFunc(h.serveTus))
	}
}

// PublicRoutes mounts the unauthenticated public-bucket fast-path. Mount
// at the root router (NOT under /api); requests skip middleware that
// would otherwise reject anonymous access.
func (h *StorageHandler) PublicRoutes(r chi.Router) {
	r.Get("/storage/v1/object/public/{projectId}/{bucket}/*", h.PublicGetObject)
}

func (h *StorageHandler) ListBuckets(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	buckets, err := h.svc.ListBuckets(r.Context(), projectID)
	if err != nil {
		storageError(w, "list buckets", err)
		return
	}
	if buckets == nil {
		buckets = []storagesvc.Bucket{}
	}
	writeJSON(w, buckets)
}

func (h *StorageHandler) CreateBucket(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	var req storagesvc.CreateBucketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	bucket, err := h.svc.CreateBucket(r.Context(), projectID, req)
	if err != nil {
		storageError(w, "create bucket", err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, bucket)
}

func (h *StorageHandler) DeleteBucket(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	if err := h.svc.DeleteBucket(r.Context(), projectID, bucketName); err != nil {
		storageError(w, "delete bucket", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *StorageHandler) ListObjects(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			httpError(w, "limit must be an integer", http.StatusBadRequest)
			return
		}
		limit = n
	}
	out, err := h.svc.ListObjects(r.Context(), projectID, bucketName, storagesvc.ListObjectsRequest{
		Prefix: q.Get("prefix"),
		Cursor: q.Get("cursor"),
		Limit:  limit,
	})
	if err != nil {
		storageError(w, "list objects", err)
		return
	}
	writeJSON(w, out)
}

func (h *StorageHandler) SignUploadURL(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	var req storagesvc.UploadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	tier, err := h.tierFor(projectID)
	if err != nil {
		storageError(w, "read project tier", err)
		return
	}
	out, err := h.svc.SignUploadURL(r.Context(), projectID, bucketName, tier, req)
	if err != nil {
		storageError(w, "sign upload url", err)
		return
	}
	writeJSON(w, out)
}

func (h *StorageHandler) ConfirmUpload(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	var req storagesvc.ConfirmUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	user := auth.GetUser(r.Context())
	ownerID := ""
	if user != nil {
		ownerID = user.ID
	}
	tier, err := h.tierFor(projectID)
	if err != nil {
		storageError(w, "read project tier", err)
		return
	}
	obj, err := h.svc.ConfirmUpload(r.Context(), projectID, bucketName, tier, ownerID, req)
	if err != nil {
		storageError(w, "confirm upload", err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, obj)
}

func (h *StorageHandler) SignDownloadURL(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	key := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	out, err := h.svc.SignDownloadURL(r.Context(), projectID, bucketName, key)
	if err != nil {
		storageError(w, "sign download url", err)
		return
	}
	writeJSON(w, out)
}

func (h *StorageHandler) GetObjectMetadata(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	key := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	// We're not exposing the head-from-R2 path here — clients can derive
	// metadata from ListObjects which is cheaper. This endpoint is purely
	// a "does this exist" probe used by the studio.
	resp, err := h.svc.ListObjects(r.Context(), projectID, bucketName, storagesvc.ListObjectsRequest{
		Prefix: key, Limit: 1,
	})
	if err != nil {
		storageError(w, "object metadata", err)
		return
	}
	for _, o := range resp.Objects {
		if o.Key == key {
			writeJSON(w, o)
			return
		}
	}
	httpError(w, errObjectNotFound, http.StatusNotFound)
}

func (h *StorageHandler) DeleteObject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	key := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if err := h.svc.DeleteObject(r.Context(), projectID, bucketName, key); err != nil {
		storageError(w, "delete object", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PublicGetObject serves anonymous reads from public buckets via a 302
// redirect to the R2 public URL. We redirect rather than proxying bytes
// so Excalibase doesn't pay egress on every download. Cloudflare edge
// caches the redirect target.
func (h *StorageHandler) PublicGetObject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	key := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	out, err := h.svc.SignDownloadURL(r.Context(), projectID, bucketName, key)
	if err != nil {
		storageError(w, "public object", err)
		return
	}
	if !out.Public {
		// Bucket is private — refuse on the public path.
		httpError(w, "not a public bucket", http.StatusForbidden)
		return
	}
	http.Redirect(w, r, out.URL, http.StatusFound)
}

// tierFor looks up the project's tier, which decides the storage quota. A
// store failure is reported, never guessed around: answering FREE for a
// lookup that did not happen would silently apply the wrong cap. A project
// with no instance row, or no tier on it, genuinely has the FREE allowance —
// the tightest one — so that is not a guess.
func (h *StorageHandler) tierFor(projectID string) (string, error) {
	const freeTier = "FREE"
	if h.store == nil {
		return freeTier, nil
	}
	inst, err := h.store.FindByProjectID(projectID)
	if err != nil {
		return "", fmt.Errorf("look up project tier: %w", err)
	}
	if inst == nil || inst.Tier == "" {
		return freeTier, nil
	}
	return string(inst.Tier), nil
}

// --- Phase 10: ctx.storage internal routes ---

// InternalRoutes mounts the runtime-token-authenticated routes that back
// `ctx.storage` in the Deno runtime. Mount at the root router:
//
//	r.Route("/internal/storage", func(r chi.Router) { h.InternalRoutes(r) })
//
// Routes (all auth via X-Excalibase-Runtime-Token header):
//
//	POST   /{projectId}/upload-url       mint a signed PUT URL + storageId
//	POST   /{projectId}/confirm-upload   record metadata after PUT completes
//	POST   /{projectId}/download-url     mint a signed GET URL for a storageId
//	GET    /{projectId}/metadata/{id}    fetch metadata for a storageId
//	DELETE /{projectId}/{id}             remove a storageId (idempotent)
func (h *StorageHandler) InternalRoutes(r chi.Router) {
	r.Post("/internal/storage/{projectId}/upload-url", h.InternalSignUploadURL)
	r.Post("/internal/storage/{projectId}/confirm-upload", h.InternalConfirmUpload)
	r.Post("/internal/storage/{projectId}/download-url", h.InternalSignDownloadURL)
	r.Get("/internal/storage/{projectId}/metadata/{storageId}", h.InternalGetMetadata)
	r.Delete("/internal/storage/{projectId}/{storageId}", h.InternalDeleteObject)
}

// requireRuntimeToken is the shared auth check for /internal/storage/*.
// Constant-time comparison defends against timing side channels.
// Returns true when the request is authorised.
func (h *StorageHandler) requireRuntimeToken(w http.ResponseWriter, r *http.Request) bool {
	if h.runtimeSecret == "" {
		httpError(w, errUnauthorized, http.StatusUnauthorized)
		return false
	}
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return false
	}
	// Per-project: the token must match the derived secret for the project in
	// the path, so a token minted for one project can't act on another (SEC-C5).
	expected := edgefn.DeriveRuntimeSecret(h.runtimeSecret, projectID)
	provided := r.Header.Get(runtimeTokenHeader)
	if len(provided) == 0 ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		httpError(w, errUnauthorized, http.StatusUnauthorized)
		return false
	}
	return true
}

// ensureCtxStorageBucket auto-provisions the conventional `_ctx_storage`
// bucket for the project on first use. Bucket is private (signed URLs
// only). Subsequent calls hit the duplicate guard inside CreateBucket
// and return nil — the caller doesn't care which branch ran.
func (h *StorageHandler) ensureCtxStorageBucket(ctx context.Context, projectID string) error {
	existing, err := h.svc.ListBuckets(ctx, projectID)
	if err != nil {
		return err
	}
	for _, b := range existing {
		if b.Name == ctxStorageBucket {
			return nil
		}
	}
	_, err = h.svc.CreateBucket(ctx, projectID, storagesvc.CreateBucketRequest{
		Name:   ctxStorageBucket,
		Public: false,
	})
	if err != nil && !errors.Is(err, storagesvc.ErrBucketExists) {
		return err
	}
	return nil
}

// mintStorageID generates a 30-character lowercase base32-ish identifier
// (we use hex for portability — the runtime treats ids as opaque). The
// `kg2_` prefix groups identifiers under one namespace.
func mintStorageID() (string, error) {
	b := make([]byte, 13)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "kg2_" + hex.EncodeToString(b), nil
}

// internalUploadURLRequest — body shape posted by the runtime to
// /internal/storage/{projectId}/upload-url. ContentType + Size are required:
// they are bound into the signed PUT and checked against the project's quota,
// so an upload with neither cannot be authorised at all.
type internalUploadURLRequest struct {
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

// internalUploadURLResponse — what the runtime expects back. The storage
// id is minted server-side and carried verbatim through the client →
// runtime → provisioning chain on the eventual confirm-upload call.
type internalUploadURLResponse struct {
	StorageID string `json:"storageId"`
	// UploadID names the staged bytes; the runtime hands it back on confirm.
	UploadID string            `json:"uploadId"`
	URL      string            `json:"url"`
	Method   string            `json:"method"`
	Headers  map[string]string `json:"headers,omitempty"`
}

// InternalSignUploadURL mints a signed PUT URL the client will upload to
// directly. Auto-provisions the per-project `_ctx_storage` bucket on
// first use so callers don't have to.
func (h *StorageHandler) InternalSignUploadURL(w http.ResponseWriter, r *http.Request) {
	if !h.requireRuntimeToken(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	var req internalUploadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if err := h.ensureCtxStorageBucket(r.Context(), projectID); err != nil {
		storageError(w, "ensure ctx-storage bucket", err)
		return
	}
	storageID, err := mintStorageID()
	if err != nil {
		httpError(w, "mint storage id", http.StatusInternalServerError)
		return
	}
	tier, err := h.tierFor(projectID)
	if err != nil {
		storageError(w, "read project tier", err)
		return
	}
	out, err := h.svc.SignUploadURL(r.Context(), projectID, ctxStorageBucket, tier,
		storagesvc.UploadURLRequest{
			Key:      storageID,
			MimeType: req.ContentType,
			Size:     req.Size,
		})
	if err != nil {
		storageError(w, "sign upload url", err)
		return
	}
	writeJSON(w, internalUploadURLResponse{
		StorageID: storageID,
		UploadID:  out.UploadID,
		URL:       out.URL,
		Method:    out.Method,
		Headers:   out.Headers,
	})
}

// internalConfirmUploadRequest names the upload that finished. Size and
// contentType are accepted for wire compatibility with the runtime but no
// longer believed: the object store is read back for both. Sha256 is the
// runtime's own digest and is kept as the catalogue ETag.
type internalConfirmUploadRequest struct {
	StorageID string `json:"storageId"`
	// UploadID is the id the upload URL was issued under; it names the bytes
	// being accepted, which are not on the storage id's key yet.
	UploadID    string `json:"uploadId"`
	ContentType string `json:"contentType,omitempty"`
	Size        int64  `json:"size"`
	Sha256      string `json:"sha256,omitempty"`
	ETag        string `json:"etag,omitempty"`
}

// InternalConfirmUpload stamps the metadata row for the storage id so
// subsequent getMetadata calls have something to return. The R2 PUT
// itself happened directly between client and R2 — provisioning only
// records the catalogue entry here.
func (h *StorageHandler) InternalConfirmUpload(w http.ResponseWriter, r *http.Request) {
	if !h.requireRuntimeToken(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	var req internalConfirmUploadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.StorageID == "" {
		httpError(w, errStorageIDRequired, http.StatusBadRequest)
		return
	}
	// We persist sha256 in the MimeType field-aware path? No — service's
	// ConfirmUpload only knows MimeType + Size + ETag + Key. To carry the
	// sha256 forward to getMetadata we tack it onto the ETag (S3 servers
	// typically populate ETag with an MD5; we use the sha256 here for
	// catalogue parity with Convex). Production deployments backed by R2
	// will see this as the recorded ETag string; the metadata reader
	// distinguishes by length / format.
	etagForCatalogue := req.ETag
	if etagForCatalogue == "" {
		etagForCatalogue = req.Sha256
	}
	tier, err := h.tierFor(projectID)
	if err != nil {
		storageError(w, "read project tier", err)
		return
	}
	_, err = h.svc.ConfirmUpload(r.Context(), projectID, ctxStorageBucket, tier, "" /* ownerID */, storagesvc.ConfirmUploadRequest{
		Key:      req.StorageID,
		UploadID: req.UploadID,
		ETag:     etagForCatalogue,
	})
	if err != nil {
		storageError(w, "confirm upload", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// internalDownloadURLRequest — body for /internal/storage/{p}/download-url.
type internalDownloadURLRequest struct {
	StorageID string `json:"storageId"`
}

// internalDownloadURLResponse — signed GET URL the worker hands back to
// the handler that called ctx.storage.getUrl.
type internalDownloadURLResponse struct {
	URL string `json:"url"`
}

// InternalSignDownloadURL mints a signed GET URL for the storage id, or
// 404s when no catalogue row exists. The runtime translates the 404 into
// `null` for `StorageReader.getUrl`'s nullable return type.
func (h *StorageHandler) InternalSignDownloadURL(w http.ResponseWriter, r *http.Request) {
	if !h.requireRuntimeToken(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	var req internalDownloadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, errInvalidBody, http.StatusBadRequest)
		return
	}
	if req.StorageID == "" {
		httpError(w, errStorageIDRequired, http.StatusBadRequest)
		return
	}
	// Confirm catalogue presence first so missing ids surface as 404
	// instead of returning a URL that would 404 on R2 later.
	obj, err := h.lookupObject(r.Context(), projectID, req.StorageID)
	if err != nil {
		storageError(w, "look up object", err)
		return
	}
	if obj == nil {
		httpError(w, errObjectNotFound, http.StatusNotFound)
		return
	}
	out, err := h.svc.SignDownloadURL(r.Context(), projectID, ctxStorageBucket, req.StorageID)
	if err != nil {
		storageError(w, "sign download url", err)
		return
	}
	writeJSON(w, internalDownloadURLResponse{URL: out.URL})
}

// internalMetadataResponse mirrors the StorageFileMetadata shape on the
// runtime side: storageId / sha256 / size / contentType. The sha256 is
// pulled from the catalogue's ETag field (see InternalConfirmUpload).
type internalMetadataResponse struct {
	StorageID   string `json:"storageId"`
	Sha256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType,omitempty"`
}

// InternalGetMetadata returns the catalogue metadata for a storage id, or
// 404 when no row exists. The runtime maps 404 → `null` to match the
// `StorageReader.getMetadata` nullable contract.
func (h *StorageHandler) InternalGetMetadata(w http.ResponseWriter, r *http.Request) {
	if !h.requireRuntimeToken(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	storageID := chi.URLParam(r, "storageId")
	if storageID == "" {
		httpError(w, errStorageIDRequired, http.StatusBadRequest)
		return
	}
	obj, err := h.lookupObject(r.Context(), projectID, storageID)
	if err != nil {
		storageError(w, "look up object", err)
		return
	}
	if obj == nil {
		httpError(w, errObjectNotFound, http.StatusNotFound)
		return
	}
	writeJSON(w, internalMetadataResponse{
		StorageID:   storageID,
		Sha256:      obj.ETag,
		Size:        obj.Size,
		ContentType: obj.MimeType,
	})
}

// InternalDeleteObject removes the storage id from both the blob store and
// the catalogue; 204 means both happened. Idempotent in the only sense that
// is safe: an id whose bucket or object is already gone is success, because
// nothing is left to remove. A blob-store or catalogue failure is a 5xx, so
// the runtime retries instead of believing the object is gone.
func (h *StorageHandler) InternalDeleteObject(w http.ResponseWriter, r *http.Request) {
	if !h.requireRuntimeToken(w, r) {
		return
	}
	projectID := chi.URLParam(r, "projectId")
	if err := edgefn.ValidateProjectID(projectID); err != nil {
		httpError(w, safeError(err), http.StatusBadRequest)
		return
	}
	storageID := chi.URLParam(r, "storageId")
	if storageID == "" {
		httpError(w, errStorageIDRequired, http.StatusBadRequest)
		return
	}
	err := h.svc.DeleteObject(r.Context(), projectID, ctxStorageBucket, storageID)
	if err != nil && !errors.Is(err, storagesvc.ErrBucketNotFound) {
		storageError(w, "delete object", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lookupObject finds the catalogue row for (project, storageID) within the
// ctx-storage bucket. ListObjects with prefix=key is the lightest path
// that doesn't require a new BucketStore method.
func (h *StorageHandler) lookupObject(ctx context.Context, projectID, storageID string) (*storagesvc.Object, error) {
	resp, err := h.svc.ListObjects(ctx, projectID, ctxStorageBucket, storagesvc.ListObjectsRequest{
		Prefix: storageID, Limit: 1,
	})
	if err != nil {
		// A bucket that was never auto-provisioned holds no objects.
		if errors.Is(err, storagesvc.ErrBucketNotFound) {
			return nil, nil
		}
		return nil, err
	}
	for i := range resp.Objects {
		if resp.Objects[i].Key == storageID {
			return &resp.Objects[i], nil
		}
	}
	return nil, nil
}

const errUnauthorized = "unauthorized"
