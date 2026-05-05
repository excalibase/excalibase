package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// AWSS3Uploader is the production S3Uploader. It uses
// aws-sdk-go-v2's TransferManager so multi-GB backups stream in
// 5MB parts (R2's minimum) and abort cleanly on context cancel —
// the manager calls AbortMultipartUpload internally so we don't
// pay for orphaned bytes.
type AWSS3Uploader struct {
	client *s3.Client
}

// AWSS3UploaderConfig configures the uploader. Region is required by
// the SDK but R2 ignores it; "auto" works.
type AWSS3UploaderConfig struct {
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	Endpoint        string // R2 / MinIO / LocalStack endpoint; empty for real AWS S3
	// UsePathStyle controls bucket addressing:
	//   true  — https://<endpoint>/<bucket>/<key>  (R2, MinIO, LocalStack)
	//   false — https://<bucket>.<endpoint>/<key>  (AWS S3 — virtual-host)
	// R2 needs path-style on because the TLS cert covers the shared
	// account endpoint, not per-bucket subdomains. Match the existing
	// storagesvc.R2Client behaviour. Default in production is true.
	UsePathStyle bool
}

func NewAWSS3Uploader(ctx context.Context, c AWSS3UploaderConfig) (*AWSS3Uploader, error) {
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, errors.New("backup uploader: AccessKeyID and SecretAccessKey are required")
	}
	region := c.Region
	if region == "" {
		region = "auto"
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if c.Endpoint != "" {
			o.BaseEndpoint = aws.String(c.Endpoint)
		}
		o.UsePathStyle = c.UsePathStyle
	})
	return &AWSS3Uploader{client: client}, nil
}

func (u *AWSS3Uploader) Upload(ctx context.Context, bucket, key string, body io.Reader) (int64, error) {
	uploader := manager.NewUploader(u.client, func(opts *manager.Uploader) {
		// 5MiB part — R2's minimum-but-not-maximum. Smaller parts give
		// finer cancellation granularity at the cost of extra requests.
		opts.PartSize = 5 * 1024 * 1024
	})
	_, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	if err != nil {
		return 0, fmt.Errorf("s3 upload %s/%s: %w", bucket, key, err)
	}
	// HeadObject is the only way to get the final object size cheaply
	// without buffering. Cheaper than ListObjects on a single key.
	head, err := u.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, nil // size unknown, but the upload itself succeeded
	}
	if head.ContentLength == nil {
		return 0, nil
	}
	return *head.ContentLength, nil
}

func (u *AWSS3Uploader) Download(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	out, err := u.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("s3 download %s/%s: %w", bucket, key, err)
	}
	return out.Body, nil
}

func (u *AWSS3Uploader) Delete(ctx context.Context, bucket, key string) error {
	_, err := u.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nf *types.NoSuchKey
		if errors.As(err, &nf) {
			return nil
		}
		return fmt.Errorf("s3 delete %s/%s: %w", bucket, key, err)
	}
	return nil
}

func (u *AWSS3Uploader) List(ctx context.Context, bucket, prefix string) ([]S3Object, error) {
	paginator := s3.NewListObjectsV2Paginator(u.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String(prefix),
	})
	var out []S3Object
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("s3 list %s/%s: %w", bucket, prefix, err)
		}
		for _, obj := range page.Contents {
			size := int64(0)
			if obj.Size != nil {
				size = *obj.Size
			}
			key := ""
			if obj.Key != nil {
				key = *obj.Key
			}
			out = append(out, S3Object{Key: key, SizeBytes: size})
		}
	}
	return out, nil
}
