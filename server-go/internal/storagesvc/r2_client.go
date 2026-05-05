package storagesvc

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

// objectKey builds the storage key for a (project, bucket, user-key)
// tuple. Single platform R2 bucket is partitioned by this prefix:
//
//	projects/<projectId>/buckets/<bucket>/<key>
//
// The key is path-cleaned but NOT URL-encoded — R2 stores raw UTF-8.
// We DO refuse "/.." sequences so callers can't escape the prefix.
func objectKey(projectID, bucket, userKey string) (string, error) {
	if projectID == "" || bucket == "" {
		return "", fmt.Errorf("r2: project + bucket required")
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
	return fmt.Sprintf("projects/%s/buckets/%s%s", projectID, bucket, cleaned), nil
}

// SignedPutURL returns a presigned URL the client can PUT bytes to
// directly. Excalibase never streams the file content. ttl bounds how
// long the URL is valid; default 5 min if ttl == 0.
//
// Content-Type is encoded into the signed URL — the client MUST PUT with
// that exact header or R2 rejects the request. This binds the URL to a
// specific MIME type, preventing image-bucket → executable-upload tricks.
func (r *R2Client) SignedPutURL(ctx context.Context, projectID, bucket, key, mimeType string, ttl time.Duration) (string, time.Time, error) {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	storeKey, err := objectKey(projectID, bucket, key)
	if err != nil {
		return "", time.Time{}, err
	}
	in := &s3.PutObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(storeKey),
	}
	if mimeType != "" {
		in.ContentType = aws.String(mimeType)
	}
	signed, err := r.presign.PresignPutObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("presign put: %w", err)
	}
	return signed.URL, time.Now().Add(ttl), nil
}

// SignedGetURL returns a presigned URL the client can GET bytes from.
// Used for private buckets; public buckets return PublicURL() instead.
func (r *R2Client) SignedGetURL(ctx context.Context, projectID, bucket, key string, ttl time.Duration) (string, time.Time, error) {
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	storeKey, err := objectKey(projectID, bucket, key)
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
func (r *R2Client) PublicURL(projectID, bucket, key string) string {
	storeKey, err := objectKey(projectID, bucket, key)
	if err != nil {
		return ""
	}
	if r.cfg.PublicURL != "" {
		return strings.TrimRight(r.cfg.PublicURL, "/") + "/" + storeKey
	}
	// Fallback: R2's default pub-<hash>.r2.dev requires explicit per-bucket
	// public access enable in the Cloudflare dashboard. Without that,
	// callers SHOULD configure PublicURL or use signed URLs.
	return fmt.Sprintf("%s/%s/%s", strings.TrimRight(r.cfg.Endpoint, "/"), url.PathEscape(r.cfg.Bucket), storeKey)
}

// DeleteObject removes a single key. Used on object delete and as part of
// bucket-cascade-delete (caller iterates keys + calls this).
func (r *R2Client) DeleteObject(ctx context.Context, projectID, bucket, key string) error {
	storeKey, err := objectKey(projectID, bucket, key)
	if err != nil {
		return err
	}
	_, err = r.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(storeKey),
	})
	if err != nil {
		return fmt.Errorf("delete object: %w", err)
	}
	return nil
}

// HeadObject queries R2 for the size + content-type of an existing object.
// Used after the client confirms upload, to validate what they claimed
// matches what's actually in R2.
func (r *R2Client) HeadObject(ctx context.Context, projectID, bucket, key string) (size int64, contentType, etag string, err error) {
	storeKey, err := objectKey(projectID, bucket, key)
	if err != nil {
		return 0, "", "", err
	}
	out, err := r.s3.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(storeKey),
	})
	if err != nil {
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
