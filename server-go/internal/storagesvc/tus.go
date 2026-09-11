package storagesvc

import (
	"context"
	"errors"

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
	if s.r2 == nil {
		return nil
	}
	store := s3store.New(s.r2.cfg.Bucket, s.r2.s3)
	composer := tusd.NewStoreComposer()
	store.UseIn(composer)
	memorylocker.New().UseIn(composer)
	return composer
}

// StartResumableUpload validates a resumable upload against its target bucket
// (existence, MIME allowlist, per-project quota) and returns the S3 object key
// the bytes must land at. It mirrors SignUploadURL minus the presign step, so a
// tus upload is subject to the same guardrails as a single-PUT upload.
func (s *Service) StartResumableUpload(ctx context.Context, projectID, bucketName, tier string, req UploadURLRequest) (string, error) {
	bucket, err := s.store.GetBucket(ctx, projectID, bucketName)
	if err != nil {
		return "", err
	}
	if bucket == nil {
		return "", errors.New(errBucketNotFound)
	}
	if err := s.validateUploadRequest(ctx, projectID, tier, bucket, req); err != nil {
		return "", err
	}
	return objectKey(projectID, bucketName, req.Key)
}
