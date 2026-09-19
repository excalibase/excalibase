//go:build integration

package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/go-chi/chi/v5"
	testcontainers "github.com/testcontainers/testcontainers-go"
	tclocalstack "github.com/testcontainers/testcontainers-go/modules/localstack"
	tusd "github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
	"github.com/tus/tusd/v2/pkg/s3store"
)

// TestTus_ResumableMultipartUpload_LandsObjectAndMetadata exercises the whole
// resumable path against a real S3 backend (LocalStack): a multi-part upload
// driven in two PATCH requests lands the object at the canonical R2 key layout
// AND records the metadata row, so it is indistinguishable from a presigned
// upload on list/download. Gated behind the `integration` build tag.
func TestTus_ResumableMultipartUpload_LandsObjectAndMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const platformBucket = "excalibase-int"
	s3Client, endpoint := startLocalStackS3(ctx, t, platformBucket)

	// Bucket store + service backed by an R2 client pointed at LocalStack.
	store := newInMemoryBucketStoreForTest()
	r2, err := storagesvc.NewR2Client(storagesvc.R2Config{
		AccessKeyID: "test", SecretAccessKey: "test", Region: "us-east-1",
		Endpoint: endpoint, Bucket: platformBucket,
	})
	if err != nil {
		t.Fatalf("r2 client: %v", err)
	}
	svc := storagesvc.NewService(store, r2, nil)
	h := NewStorageHandler(svc, nil)

	// Force real multipart: 5 MiB parts so a 6 MiB upload lands as 2 S3 parts.
	tusStore := s3store.New(platformBucket, s3Client)
	tusStore.PreferredPartSize = 5 * 1024 * 1024
	tusStore.MinPartSize = 5 * 1024 * 1024
	composer := tusd.NewStoreComposer()
	tusStore.UseIn(composer)
	memorylocker.New().UseIn(composer)
	if err := h.EnableResumableUploads(composer); err != nil {
		t.Fatalf("enable resumable uploads: %v", err)
	}

	// A logical bucket must exist for the pre-create validation to pass.
	_ = store.CreateBucket(ctx, &storagesvc.Bucket{ID: "b1", ProjectID: "proj1", Name: "media"})

	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/storage", func(r chi.Router) { h.Routes(r) })

	payload := make([]byte, 6*1024*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	// 1) Create the upload.
	md := strings.Join([]string{
		"bucket " + b64("media"),
		"key " + b64("clips/big.bin"),
		"filetype " + b64("application/octet-stream"),
	}, ",")
	createReq := httptest.NewRequest("POST", "/api/projects/proj1/storage/tus", nil)
	createReq.Header.Set("Tus-Resumable", "1.0.0")
	createReq.Header.Set("Upload-Length", strconv.Itoa(len(payload)))
	createReq.Header.Set("Upload-Metadata", md)
	createResp := httptest.NewRecorder()
	r.ServeHTTP(createResp, createReq)
	if createResp.Code != http.StatusCreated {
		t.Fatalf("create: got %d body=%s", createResp.Code, createResp.Body.String())
	}
	location := createResp.Header().Get("Location")
	if location == "" {
		t.Fatal("create: missing Location header")
	}
	// Location must route back under the project-scoped mount.
	uploadPath := location[strings.Index(location, "/api/"):]
	if !strings.HasPrefix(uploadPath, "/api/projects/proj1/storage/tus/") {
		t.Fatalf("Location not under mount path: %s", location)
	}

	// 2) Upload in two PATCH requests (resumable) to exercise offset resume.
	half := len(payload) / 2
	patchAt(t, r, uploadPath, 0, payload[:half])
	patchAt(t, r, uploadPath, half, payload[half:])

	// 3) Object present in S3 at the canonical key.
	// Keys are namespaced by the bucket id, not its name.
	const wantKey = "projects/proj1/buckets/b1/clips/big.bin"
	head, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(platformBucket), Key: aws.String(wantKey),
	})
	if err != nil {
		t.Fatalf("HeadObject %s: %v", wantKey, err)
	}
	if head.ContentLength == nil || *head.ContentLength != int64(len(payload)) {
		t.Fatalf("S3 object size = %v, want %d", head.ContentLength, len(payload))
	}

	// 4) Metadata row recorded (completion hook), same shape as ConfirmUpload.
	obj, err := store.GetObject(ctx, "b1", "clips/big.bin")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if obj == nil {
		t.Fatal("expected metadata row after completion, got nil")
	}
	if obj.Size != int64(len(payload)) {
		t.Errorf("metadata size = %d, want %d", obj.Size, len(payload))
	}
}

func patchAt(t *testing.T, r chi.Router, path string, offset int, chunk []byte) {
	t.Helper()
	req := httptest.NewRequest("PATCH", path, bytes.NewReader(chunk))
	req.Header.Set("Tus-Resumable", "1.0.0")
	req.Header.Set("Content-Type", "application/offset+octet-stream")
	req.Header.Set("Upload-Offset", strconv.Itoa(offset))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("PATCH @%d: got %d body=%s", offset, w.Code, w.Body.String())
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func startLocalStackS3(ctx context.Context, t *testing.T, bucket string) (*s3.Client, string) {
	t.Helper()
	lsC, err := tclocalstack.Run(ctx, "localstack/localstack:3.7",
		testcontainers.WithEnv(map[string]string{"SERVICES": "s3"}))
	if err != nil {
		t.Fatalf("start localstack: %v", err)
	}
	t.Cleanup(func() { _ = lsC.Terminate(ctx) })
	host, _ := lsC.Host(ctx)
	port, _ := lsC.MappedPort(ctx, "4566/tcp")
	endpoint := fmt.Sprintf("http://%s:%s", host, port.Port())

	client := s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  awscreds.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
	})
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create platform bucket: %v", err)
	}
	return client, endpoint
}
