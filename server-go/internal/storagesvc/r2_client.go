package storagesvc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// R2Config holds the credentials + endpoint for Cloudflare R2 (or any
// S3-compatible store). Reads from the K8s secret r2-creds via
// SetR2Config in main.go.
type R2Config struct {
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string // e.g. https://<account_id>.r2.cloudflarestorage.com
	Region          string // R2 ignores; SDK requires non-empty. Default "auto".
	Bucket          string // single platform bucket; per-project namespace via key prefix
	PublicURL       string // optional CDN/custom domain in front of public objects
}

// R2Client wraps the S3 SDK against an R2 endpoint. The presign client
// is kept separately because it has its own request-signing flow.
type R2Client struct {
	cfg     R2Config
	s3      *s3.Client
	presign *s3.PresignClient
}

// NewR2Client constructs the SDK clients. UsePathStyle=true is required for
// R2 (and most non-AWS S3 implementations) because the virtual-hosted-style
// (bucket.endpoint) addressing fails the TLS SAN check when the endpoint
// hostname is shared across buckets.
func NewR2Client(cfg R2Config) (*R2Client, error) {
	if cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("r2: access key + secret required")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("r2: endpoint URL required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("r2: bucket required")
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}

	awsCfg := aws.Config{
		Region: cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, "",
		),
	}
	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true
	})
	return &R2Client{
		cfg:     cfg,
		s3:      s3Client,
		presign: s3.NewPresignClient(s3Client),
	}, nil
}

// bucketPrefix is the key namespace holding every object of one bucket.
//
// It is keyed by the bucket's id, never its name. A presigned PUT lives for
// its whole TTL and cannot be recalled: with a name-based prefix, deleting a
// bucket and re-creating it under the same name would hand an outstanding URL
// a write into the new bucket's space — bytes with no catalogue row, inside a
// live bucket whose emptiness was already verified. Ids are never reused, so
// an outstanding URL can only ever address the bucket it was issued for, and
// after that bucket is gone its writes land in a namespace nothing reads.
//
// The trailing slash is load-bearing too: without it a prefix listing for one
// id would also match any id that starts with it.
func bucketPrefix(projectID, bucketID string) (string, error) {
	if projectID == "" || bucketID == "" {
		return "", fmt.Errorf("r2: project + bucket id required")
	}
	return fmt.Sprintf("projects/%s/buckets/%s/", projectID, bucketID), nil
}

// objectKey builds the storage key for a (project, bucket id, user-key)
// tuple. Single platform R2 bucket is partitioned by this prefix:
//
//	projects/<projectId>/buckets/<bucketId>/<key>
//
// The key is path-cleaned but NOT URL-encoded — R2 stores raw UTF-8.
// We DO refuse "/.." sequences so callers can't escape the prefix.
func objectKey(projectID, bucketID, userKey string) (string, error) {
	prefix, err := bucketPrefix(projectID, bucketID)
	if err != nil {
		return "", err
	}
	// Refuse traversal at the segment level — `path.Clean` would turn
	// "foo/../bar" into "bar" silently, which is technically safe but
	// surprises users who think their key contains "..". We refuse
	// before cleaning so the storage layout matches the user's intent.
	for _, seg := range strings.Split(userKey, "/") {
		if seg == ".." {
			return "", fmt.Errorf("r2: key contains traversal: %q", userKey)
		}
	}
	cleaned := path.Clean("/" + userKey)
	if cleaned == "/" || cleaned == "" {
		return "", fmt.Errorf("r2: invalid key %q", userKey)
	}
	return prefix + strings.TrimPrefix(cleaned, "/"), nil
}

// SignedPutURL returns a presigned URL the client can PUT bytes to
// directly. Excalibase never streams the file content. ttl bounds how
// long the URL is valid; default 5 min if ttl == 0.
//
// Both Content-Type and Content-Length are part of the signature (they
// appear in X-Amz-SignedHeaders), so the URL authorises one object of one
// type at one exact length: a client that sends different headers, or a
// different number of bytes, gets a signature mismatch from the object store
// rather than an upload. Size must be positive — an unbound length is the
// bypass this closes.
func (r *R2Client) SignedPutURL(ctx context.Context, projectID, bucketID, key, mimeType string, size int64, ttl time.Duration) (string, time.Time, error) {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if mimeType == "" {
		return "", time.Time{}, fmt.Errorf("r2: content type required to sign an upload")
	}
	if size <= 0 {
		return "", time.Time{}, fmt.Errorf("r2: content length required to sign an upload")
	}
	storeKey, err := objectKey(projectID, bucketID, key)
	if err != nil {
		return "", time.Time{}, err
	}
	signed, err := r.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(r.cfg.Bucket),
		Key:           aws.String(storeKey),
		ContentType:   aws.String(mimeType),
		ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presign put: %w", err)
	}
	return signed.URL, time.Now().Add(ttl), nil
}

// SignedGetURL returns a presigned URL the client can GET bytes from.
// Used for private buckets; public buckets return PublicURL() instead.
func (r *R2Client) SignedGetURL(ctx context.Context, projectID, bucketID, key string, ttl time.Duration) (string, time.Time, error) {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	storeKey, err := objectKey(projectID, bucketID, key)
	if err != nil {
		return "", time.Time{}, err
	}
	signed, err := r.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(storeKey),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presign get: %w", err)
	}
	return signed.URL, time.Now().Add(ttl), nil
}

// PublicURL returns the static URL for a public-bucket object. Falls back
// to the R2 public bucket URL pattern when no custom domain is configured.
// Caller is responsible for ensuring the bucket is actually public — this
// function just constructs a URL string.
func (r *R2Client) PublicURL(projectID, bucketID, key string) (string, error) {
	storeKey, err := objectKey(projectID, bucketID, key)
	if err != nil {
		return "", err
	}
	if r.cfg.PublicURL != "" {
		return strings.TrimRight(r.cfg.PublicURL, "/") + "/" + storeKey, nil
	}
	// Fallback: R2's default pub-<hash>.r2.dev requires explicit per-bucket
	// public access enable in the Cloudflare dashboard. Without that,
	// callers SHOULD configure PublicURL or use signed URLs.
	return fmt.Sprintf("%s/%s/%s", strings.TrimRight(r.cfg.Endpoint, "/"), url.PathEscape(r.cfg.Bucket), storeKey), nil
}

// DeleteObject removes a single key. Used on object delete and as part of
// bucket-cascade-delete (caller iterates keys + calls this). An object that
// is already absent counts as deleted, so a retried delete converges instead
// of failing forever on the second attempt.
func (r *R2Client) DeleteObject(ctx context.Context, projectID, bucketID, key string) error {
	storeKey, err := objectKey(projectID, bucketID, key)
	if err != nil {
		return err
	}
	_, err = r.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(storeKey),
	})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

// StoredObject is one key as the object store holds it: the key relative to
// its bucket, plus its size and when it was last written. The reaper compares
// the write time against the catalogue to find uploads that were never
// confirmed; a bucket delete uses the same listing to prove the blob plane is
// really empty before the catalogue is dropped, which the catalogue alone
// cannot prove.
type StoredObject struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// ListObjects returns what the object store holds under one bucket's prefix.
// Keys come back relative to the bucket, matching the catalogue's view.
func (r *R2Client) ListObjects(ctx context.Context, projectID, bucketID string, limit int32) ([]StoredObject, error) {
	prefix, err := bucketPrefix(projectID, bucketID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	out, err := r.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(r.cfg.Bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	objects := make([]StoredObject, 0, len(out.Contents))
	for _, o := range out.Contents {
		if o.Key == nil {
			continue
		}
		obj := StoredObject{Key: strings.TrimPrefix(*o.Key, prefix)}
		if o.Size != nil {
			obj.Size = *o.Size
		}
		if o.LastModified != nil {
			obj.LastModified = *o.LastModified
		}
		objects = append(objects, obj)
	}
	return objects, nil
}

// isNotFound reports whether an S3 error means "the key isn't there". R2
// answers a delete of a missing key with 204, but other S3-compatible
// backends return NoSuchKey — both mean the same thing. The modelled error
// shapes differ per operation, so the wire code is what identifies it.
func isNotFound(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NoSuchKey", "NotFound", "NoSuchBucket":
		return true
	}
	return false
}

// HeadObject queries R2 for the size + content-type of an existing object.
// Used after the client confirms upload, to validate what they claimed
// matches what's actually in R2.
func (r *R2Client) HeadObject(ctx context.Context, projectID, bucketID, key string) (size int64, contentType, etag string, err error) {
	storeKey, err := objectKey(projectID, bucketID, key)
	if err != nil {
		return 0, "", "", err
	}
	out, err := r.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(storeKey),
	})
	if err != nil {
		// "there is nothing at this key" is a sentinel the service acts on;
		// anything else is a real failure to reach the store.
		if isNotFound(err) {
			return 0, "", "", fmt.Errorf("head object %q: %w", key, ErrObjectNotFound)
		}
		return 0, "", "", fmt.Errorf("head object: %w", err)
	}
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	if out.ContentType != nil {
		contentType = *out.ContentType
	}
	if out.ETag != nil {
		etag = strings.Trim(*out.ETag, "\"")
	}
	return size, contentType, etag, nil
}
