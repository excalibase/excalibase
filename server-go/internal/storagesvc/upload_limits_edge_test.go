package storagesvc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)


// A quota that cannot be read is not a quota of zero.
func TestService_SignUploadURL_ReportsQuotaReadFailure(t *testing.T) {
	store := newErrStore()
	backend := newStubBackend()
	svc := serviceOverStub(t, store, backend, map[string]int64{"free": 1024})
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	store.quotaReadErr = errors.New("platform db unavailable")

	_, err := svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "a.txt", MimeType: "text/plain", Size: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "read quota") {
		t.Fatalf("want a quota-read failure, got %v", err)
	}
}
func TestService_ConfirmUpload_ReportsQuotaChargeFailure(t *testing.T) {
	store := newErrStore()
	backend := newStubBackend()
	backend.put("a.txt", 10, "text/plain")
	svc := serviceOverStub(t, store, backend, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})
	store.recordObjectErr = errors.New("platform db unavailable")

	_, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "a.txt"})
	if err == nil || !strings.Contains(err.Error(), "record object") {
		t.Fatalf("want a record failure, got %v", err)
	}
}

// An object whose stored type is unusable cannot be checked against the
// allow-list, so it is removed rather than admitted.
func TestService_ConfirmUpload_RejectsUnusableStoredType(t *testing.T) {
	backend := newStubBackend()
	backend.put("a.bin", 10, "not a media type")
	svc, _ := newStubbedService(t, backend, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})

	if _, err := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "a.bin"}); err == nil {
		t.Fatal("an object with no usable content type must not be recorded")
	}
	if len(backend.deletedKeys()) == 0 {
		t.Error("the unusable object must be deleted")
	}
}

// A transport failure on the read-back is a platform fault, not a rejection
// of the caller's upload.
func TestService_ConfirmUpload_ReportsInspectFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	r2, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	svc := NewService(newMemStore(), r2, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})

	_, confirmErr := svc.ConfirmUpload(ctx, testProjX, "files", "FREE", "u", ConfirmUploadRequest{Key: "a.txt"})
	if confirmErr == nil || !strings.Contains(confirmErr.Error(), "inspect uploaded object") {
		t.Fatalf("want an inspect failure, got %v", confirmErr)
	}
	var invalid *ValidationError
	if errors.As(confirmErr, &invalid) {
		t.Error("a platform failure must not be reported as the caller's mistake")
	}
}

// When the rejected object cannot be removed, the caller still learns why the
// upload was refused and the cleanup failure is reported alongside it.
func TestService_ConfirmUpload_ReportsCleanupFailureWithReason(t *testing.T) {
	backend := &deleteRefusingBackend{stubbedObjectBackend: newStubBackend()}
	backend.put("payload", 5*1024*1024, "image/png")
	srv := httptest.NewServer(backend.handler())
	t.Cleanup(srv.Close)
	r2, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	svc := NewService(newMemStore(), r2, map[string]int64{"free": 1024})
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "images"})

	confirmErr := func() error {
		_, e := svc.ConfirmUpload(ctx, testProjX, "images", "FREE", "u", ConfirmUploadRequest{Key: "payload"})
		return e
	}()
	if confirmErr == nil {
		t.Fatal("the over-quota upload must still be refused")
	}
	if !strings.Contains(confirmErr.Error(), "quota exceeded") {
		t.Errorf("the reason must survive the failed cleanup: %v", confirmErr)
	}
	if !strings.Contains(confirmErr.Error(), "delete rejected object") {
		t.Errorf("the failed cleanup must be reported: %v", confirmErr)
	}
}

// deleteRefusingBackend is the stub with a blob plane that refuses deletes.
type deleteRefusingBackend struct {
	*stubbedObjectBackend
}

func (b *deleteRefusingBackend) handler() http.HandlerFunc {
	inner := b.stubbedObjectBackend.handler()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		inner(w, r)
	}
}

func TestService_ReapUnconfirmedUploads_ReportsListFailure(t *testing.T) {
	store := newErrStore()
	svc := serviceOverStub(t, store, newStubBackend(), nil)
	store.listBucketErr = errors.New("platform db unavailable")

	if _, err := svc.ReapUnconfirmedUploads(context.Background(), time.Hour, time.Now()); err == nil {
		t.Fatal("a failed bucket listing must not look like an empty platform")
	}
}

// One unreachable bucket is recorded, not fatal to the sweep.
func TestService_ReapUnconfirmedUploads_RecordsUnreachableBucket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	r2, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	svc := NewService(newMemStore(), r2, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})

	report, err := svc.ReapUnconfirmedUploads(ctx, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("ReapUnconfirmedUploads: %v", err)
	}
	if len(report.Failed) != 1 {
		t.Errorf("an unreachable bucket must be reported: %+v", report)
	}
}

func TestService_FirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "b", "c"); got != "b" {
		t.Errorf("got %q, want b", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}
