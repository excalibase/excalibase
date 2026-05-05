package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
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
type StorageHandler struct {
	svc   *storagesvc.Service
	store storage.InstanceStore // looks up project tier for quota enforcement
}

func NewStorageHandler(svc *storagesvc.Service, store storage.InstanceStore) *StorageHandler {
	return &StorageHandler{svc: svc, store: store}
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
		httpError(w, "list buckets: "+safeError(err), http.StatusInternalServerError)
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
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, bucket)
}

func (h *StorageHandler) DeleteBucket(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	if err := h.svc.DeleteBucket(r.Context(), projectID, bucketName); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
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
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	out, err := h.svc.ListObjects(r.Context(), projectID, bucketName, storagesvc.ListObjectsRequest{
		Prefix: q.Get("prefix"),
		Cursor: q.Get("cursor"),
		Limit:  limit,
	})
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
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
	tier := h.tierFor(projectID)
	out, err := h.svc.SignUploadURL(r.Context(), projectID, bucketName, tier, req)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
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
	obj, err := h.svc.ConfirmUpload(r.Context(), projectID, bucketName, ownerID, req)
	if err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
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
		httpError(w, err.Error(), http.StatusBadRequest)
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
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, o := range resp.Objects {
		if o.Key == key {
			writeJSON(w, o)
			return
		}
	}
	httpError(w, "object not found", http.StatusNotFound)
}

func (h *StorageHandler) DeleteObject(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	bucketName := chi.URLParam(r, "bucket")
	key := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
	if err := h.svc.DeleteObject(r.Context(), projectID, bucketName, key); err != nil {
		httpError(w, err.Error(), http.StatusBadRequest)
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
		httpError(w, err.Error(), http.StatusNotFound)
		return
	}
	if !out.Public {
		// Bucket is private — refuse on the public path.
		httpError(w, "not a public bucket", http.StatusForbidden)
		return
	}
	http.Redirect(w, r, out.URL, http.StatusFound)
}

// tierFor looks up the project's tier from the instance store. Falls
// back to FREE when missing — matches the conservative default in
// service.tierForTier so quota math doesn't suddenly become unlimited
// for orphaned projects.
func (h *StorageHandler) tierFor(projectID string) string {
	if h.store == nil {
		return "FREE"
	}
	inst, err := h.store.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return "FREE"
	}
	if inst.Tier == "" {
		return "FREE"
	}
	return string(inst.Tier)
}
