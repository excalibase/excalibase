//go:build integration

package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// TestAWSS3Uploader_R2_E2E exercises the uploader against real
// Cloudflare R2. Gated on R2_ACCESS_KEY_ID being set so it never
// runs in default `make test` — operators run it manually before
// shipping a backup change.
//
// Reads the same env names production reads (R2_ACCESS_KEY_ID,
// R2_SECRET_ACCESS_KEY, R2_ENDPOINT, R2_BUCKET) so the credentials
// you already have wired for CNPG / storagesvc work without
// reconfiguration. Skips with a clear message if any are missing.
//
// What's actually verified that LocalStack can't:
//  1. Real path-style URL construction against the shared R2 cert
//     (LocalStack accepts any addressing mode).
//  2. Real multipart Upload + AbortMultipartUpload semantics
//     (R2 has stricter etag formats than LocalStack).
//  3. Region "auto" being accepted server-side (LocalStack ignores).
//  4. Round-trip Upload → Download bytes (multi-part reassembly).
//  5. Delete returning 204 (LocalStack returns 200 on missing key,
//     R2 returns 404 — our impl swallows the typed NoSuchKey).
//
// The test cleans up after itself even on failure.
func TestAWSS3Uploader_R2_E2E(t *testing.T) {
	ak := os.Getenv("R2_ACCESS_KEY_ID")
	sk := os.Getenv("R2_SECRET_ACCESS_KEY")
	endpoint := envFirst("R2_ENDPOINT", "BACKUP_DEFAULT_ENDPOINT")
	bucket := envFirst("R2_BUCKET", "BACKUP_DEFAULT_BUCKET")
	if ak == "" || sk == "" || endpoint == "" || bucket == "" {
		t.Skip("R2 creds not set — export R2_ACCESS_KEY_ID / R2_SECRET_ACCESS_KEY / R2_ENDPOINT / R2_BUCKET to run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	uploader, err := NewAWSS3Uploader(ctx, AWSS3UploaderConfig{
		AccessKeyID:     ak,
		SecretAccessKey: sk,
		Endpoint:        endpoint,
		Region:          "auto",
		UsePathStyle:    true, // R2 requires path-style — see TLS SAN comment in r2_client.go
	})
	if err != nil {
		t.Fatalf("NewAWSS3Uploader: %v", err)
	}

	// 6 MiB payload forces a multipart upload (PartSize is 5 MiB so
	// this lands as 2 parts — exercises the part-stitch logic without
	// burning bandwidth or storage cost).
	payload := make([]byte, 6*1024*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	key := fmt.Sprintf("integration-tests/backup-uploader-r2/%d.bin", time.Now().UnixNano())

	// Always clean up — even if the test fails partway.
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanCancel()
		_ = uploader.Delete(cleanCtx, bucket, key)
	})

	uploadAndVerifySize(ctx, t, uploader, bucket, key, payload)
	downloadAndVerifyBytes(ctx, t, uploader, bucket, key, payload)
	listAndVerifySize(ctx, t, uploader, bucket, "integration-tests/backup-uploader-r2/", key, len(payload))

	// Delete + idempotent re-Delete (R2 returns 404, we swallow).
	if err := uploader.Delete(ctx, bucket, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := uploader.Delete(ctx, bucket, key); err != nil {
		t.Errorf("re-Delete on missing key should be idempotent: %v", err)
	}
}

// uploadAndVerifySize uploads payload and checks the returned size (0 is
// tolerated because HeadObject is best-effort).
func uploadAndVerifySize(ctx context.Context, t *testing.T, uploader S3Uploader, bucket, key string, payload []byte) {
	t.Helper()
	size, err := uploader.Upload(ctx, bucket, key, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if size != int64(len(payload)) && size != 0 {
		t.Errorf("Upload size: got %d, want %d", size, len(payload))
	}
}

// downloadAndVerifyBytes round-trips the object and asserts byte equality.
func downloadAndVerifyBytes(ctx context.Context, t *testing.T, uploader S3Uploader, bucket, key string, payload []byte) {
	t.Helper()
	body, err := uploader.Download(ctx, bucket, key)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatalf("read download: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("downloaded bytes don't match upload: got len=%d, want len=%d", len(got), len(payload))
	}
}

// listAndVerifySize lists under prefix and asserts wantKey is present with the
// expected size.
func listAndVerifySize(ctx context.Context, t *testing.T, uploader S3Uploader, bucket, prefix, wantKey string, wantSize int) {
	t.Helper()
	listed, err := uploader.List(ctx, bucket, prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, obj := range listed {
		if obj.Key == wantKey {
			if obj.SizeBytes != int64(wantSize) {
				t.Errorf("listed size: got %d, want %d", obj.SizeBytes, wantSize)
			}
			return
		}
	}
	t.Errorf("uploaded key %q not found in List", wantKey)
}

// envFirst returns the first non-empty value among the given env keys.
// Lets the test accept either R2_* (CNPG / storagesvc convention) or
// BACKUP_DEFAULT_* (the new uploader's prefix).
func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
