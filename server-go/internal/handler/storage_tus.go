package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
	tusd "github.com/tus/tusd/v2/pkg/handler"
)

// Resumable/multipart uploads via tusd (https://tus.io), backed by an S3Store
// pointing at R2. Large files upload reliably — resumable, parallel, ~5 TiB
// ceiling — while the presigned single-PUT flow stays intact for small files.
//
// The tus surface lives under the existing project-scoped storage mount
// (POST/PATCH/HEAD/DELETE /api/projects/{projectId}/storage/tus...), behind the
// same auth + RequireProjectAccess middleware as every other storage route.
// Two callbacks tie tus into the existing bucket/object model:
//
//   - prepareTusUpload (pre-create) validates the target bucket the same way
//     SignUploadURL does and pins the S3 object key to the canonical layout,
//     so a resumed upload lands exactly where a presigned one would.
//   - recordTusUpload (pre-finish) records the object metadata in pg_storage
//     the same way ConfirmUpload does, so tus objects appear in list/download.

// tusUploadIDKey carries the staged upload's id from the create callback to
// the completion callback through the upload's own metadata.
const tusUploadIDKey = "excalibaseUploadId"

type tusCtxKey string

// tusProjectCtxKey carries the path-derived projectId into the tusd callbacks,
// which run without the chi routing context.
const tusProjectCtxKey tusCtxKey = "tusProjectID"

// EnableResumableUploads builds the tusd handler over the given store composer
// and wires the create/finish callbacks. Call once at startup before Routes()
// is mounted. A nil composer (R2 not configured) leaves the feature disabled;
// the /tus routes are then never registered.
func (h *StorageHandler) EnableResumableUploads(composer *tusd.StoreComposer) error {
	if composer == nil {
		return nil
	}
	th, err := tusd.NewHandler(tusd.Config{
		// The real project prefix lives in the chi mount path; serveTus strips
		// it before delegating and re-injects it into the Location header.
		BasePath:                  "/",
		StoreComposer:             composer,
		PreUploadCreateCallback:   h.prepareTusUpload,
		PreFinishResponseCallback: h.recordTusUpload,
	})
	if err != nil {
		return fmt.Errorf("tusd handler: %w", err)
	}
	h.tusd = th
	return nil
}

// serveTus adapts the project-scoped chi route to tusd's routed handler, which
// assumes it owns its base path. It strips the dynamic mount prefix
// (/api/projects/{projectId}/storage/tus) so tusd sees only the upload id,
// stashes the projectId for the callbacks, and rewrites the Location header so
// the client's follow-up PATCH/HEAD routes back through the same middleware.
func (h *StorageHandler) serveTus(w http.ResponseWriter, r *http.Request) {
	projectID := chi.URLParam(r, "projectId")
	uploadID := chi.URLParam(r, "*") // "" for POST to the collection root

	mountPath := strings.TrimSuffix(r.URL.Path, uploadID)
	mountPath = strings.TrimRight(mountPath, "/") // .../storage/tus

	rr := r.Clone(context.WithValue(r.Context(), tusProjectCtxKey, projectID))
	rr.URL.Path = "/" + uploadID

	h.tusd.ServeHTTP(&tusLocationRewriter{ResponseWriter: w, mountPath: mountPath}, rr)
}

// prepareTusUpload (tusd PreUploadCreateCallback) validates the target bucket
// and pins the S3 object key to the canonical projects/{projectId}/buckets/
// {bucket}/{key} layout so the upload is downloadable through the existing
// signed-URL path.
func (h *StorageHandler) prepareTusUpload(event tusd.HookEvent) (tusd.HTTPResponse, tusd.FileInfoChanges, error) {
	ctx := event.Context
	projectID, _ := ctx.Value(tusProjectCtxKey).(string)
	bucket, key := tusBucketAndKey(event.Upload.MetaData)
	if projectID == "" || bucket == "" || key == "" {
		return tusd.HTTPResponse{}, tusd.FileInfoChanges{},
			tusd.NewError("ERR_TUS_METADATA", "resumable upload requires 'bucket' and 'key' (or 'filename') metadata", http.StatusBadRequest)
	}
	tier, err := h.tierFor(projectID)
	if err != nil {
		return tusd.HTTPResponse{}, tusd.FileInfoChanges{},
			tusd.NewError("ERR_TUS_TIER", errStorageFailed, http.StatusInternalServerError)
	}
	storeKey, uploadID, err := h.svc.StartResumableUpload(ctx, projectID, bucket, tier, storagesvc.UploadURLRequest{
		Key:      key,
		MimeType: event.Upload.MetaData["filetype"],
		Size:     event.Upload.Size,
	})
	if err != nil {
		return tusd.HTTPResponse{}, tusd.FileInfoChanges{},
			tusd.NewError("ERR_TUS_UPLOAD", safeError(err), http.StatusBadRequest)
	}
	// The upload id travels in the tus metadata, which is the only state that
	// survives from creation to completion; the completion callback confirms
	// with it, exactly as a presigned client would.
	metadata := tusd.MetaData{}
	for k, v := range event.Upload.MetaData {
		metadata[k] = v
	}
	metadata[tusUploadIDKey] = uploadID
	// Setting ID makes the S3 object key equal storeKey (ObjectPrefix is empty).
	return tusd.HTTPResponse{}, tusd.FileInfoChanges{ID: storeKey, MetaData: metadata}, nil
}

// recordTusUpload (tusd PreFinishResponseCallback) records the completed
// object's metadata in pg_storage, the same way ConfirmUpload does, before the
// client is told the upload succeeded. Runs synchronously so a metadata write
// failure surfaces to the client instead of silently orphaning the object.
func (h *StorageHandler) recordTusUpload(event tusd.HookEvent) (tusd.HTTPResponse, error) {
	ctx := event.Context
	projectID, _ := ctx.Value(tusProjectCtxKey).(string)
	bucket, key := tusBucketAndKey(event.Upload.MetaData)
	if projectID == "" || bucket == "" || key == "" {
		return tusd.HTTPResponse{}, tusd.NewError("ERR_TUS_METADATA", "missing tus metadata on completion", http.StatusBadRequest)
	}
	ownerID := ""
	if user := auth.GetUser(ctx); user != nil {
		ownerID = user.ID
	}
	// Size and type come back from the object store, not from the tus
	// metadata, so a resumable upload is held to the same limits as a
	// presigned one.
	tier, err := h.tierFor(projectID)
	if err != nil {
		return tusd.HTTPResponse{}, tusd.NewError("ERR_TUS_TIER", errStorageFailed, http.StatusInternalServerError)
	}
	if _, err := h.svc.ConfirmUpload(ctx, projectID, bucket, tier, ownerID, storagesvc.ConfirmUploadRequest{
		Key:      key,
		UploadID: event.Upload.MetaData[tusUploadIDKey],
	}); err != nil {
		return tusd.HTTPResponse{}, err
	}
	return tusd.HTTPResponse{}, nil
}

// tusBucketAndKey pulls the target bucket + object key from tus upload
// metadata. 'key' is preferred; 'filename' (the tus-js-client default) is the
// fallback so browser clients work without extra configuration.
func tusBucketAndKey(md tusd.MetaData) (bucket, key string) {
	bucket = md["bucket"]
	key = md["key"]
	if key == "" {
		key = md["filename"]
	}
	return bucket, key
}

// tusLocationRewriter re-inserts the stripped mount path into the Location
// header tusd emits on upload creation, so the client's follow-up requests hit
// the project-scoped route again. Unwrap is preserved so tusd's read/write
// deadline control (net/http.ResponseController) keeps working through the wrap.
type tusLocationRewriter struct {
	http.ResponseWriter
	mountPath string
	rewritten bool
}

func (w *tusLocationRewriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *tusLocationRewriter) WriteHeader(statusCode int) {
	if !w.rewritten {
		w.rewritten = true
		if loc := w.Header().Get("Location"); loc != "" {
			w.Header().Set("Location", injectMountPath(loc, w.mountPath))
		}
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

// injectMountPath prefixes the path of a tusd-generated absolute Location URL
// with the storage mount path. tusd emits proto://host/{id} (BasePath "/");
// the result is proto://host{mountPath}/{id}.
func injectMountPath(location, mountPath string) string {
	u, err := url.Parse(location)
	if err != nil {
		return location
	}
	u.Path = mountPath + u.Path
	return u.String()
}
