package storagesvc

import (
	"context"

	tusd "github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
	"github.com/tus/tusd/v2/pkg/s3store"
)

// TusComposer builds a tusd StoreComposer backed by the same R2/S3 client the
// presigned-PUT path uses, so resumable (multipart) uploads land at the
// identical key layout (projects/{projectId}/buckets/{bucket}/{key}) and are
// therefore indistinguishable from presigned uploads on download and list.
//
// ObjectPrefix is left empty: the per-upload object id already carries the full
// projects/... prefix (see StartResumableUpload + the handler's pre-create
// callback). An in-memory locker guards concurrent PATCHes to one upload;
// that's sufficient for a single control-plane replica. Returns nil when no R2
// client is configured, letting the caller skip mounting the tus routes.
func (s *Service) TusComposer() *tusd.StoreComposer {
	if s.tusR2 == nil {
		return nil
	}
	store := s3store.New(s.tusR2.cfg.Bucket, s.tusR2.s3)
	composer := tusd.NewStoreComposer()
	store.UseIn(composer)
	memorylocker.New().UseIn(composer)
	return composer
}

// StartResumableUpload validates a resumable upload against its target bucket
// (existence, MIME allowlist, per-project quota) and returns the S3 object key
// the bytes must land at. It mirrors SignUploadURL minus the presign step, so a
// tus upload is subject to the same guardrails as a single-PUT upload.
// The upload lands on a staging key of its own, exactly as a presigned PUT
// does, and becomes the object only when it is confirmed. Returns the store
// key the bytes must land at and the upload id that names them.
func (s *Service) StartResumableUpload(ctx context.Context, projectID, bucketName, tier string, req UploadURLRequest) (string, string, error) {
	bucket, err := s.uploadTarget(ctx, projectID, bucketName)
	if err != nil {
		return "", "", err
	}
	if err := validateObjectKey(req.Key); err != nil {
		return "", "", err
	}
	if err := s.validateUploadRequest(ctx, projectID, tier, bucket, req); err != nil {
		return "", "", err
	}
	uploadID, err := randomID("upl")
	if err != nil {
		return "", "", err
	}
	storeKey, err := objectKey(projectID, bucket.ID, stagingObjectKey(uploadID))
	if err != nil {
		return "", "", err
	}
	return storeKey, uploadID, nil
}
