package storagesvc

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// memBucketStore is a tiny in-memory BucketStore for exercising the service
// without a SQL backend. Mirrors the handler package's test store.
type memBucketStore struct {
	mu      sync.Mutex
	buckets map[string]*Bucket
	objects map[string]map[string]*Object
	quotas  map[string]int64
}

func newMemBucketStore() *memBucketStore {
	return &memBucketStore{
		buckets: map[string]*Bucket{},
		objects: map[string]map[string]*Object{},
		quotas:  map[string]int64{},
	}
}

func (m *memBucketStore) CreateBucket(_ context.Context, b *Bucket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.buckets[b.ID] = b
	m.objects[b.ID] = map[string]*Object{}
	return nil
}

func (m *memBucketStore) GetBucket(_ context.Context, projectID, name string) (*Bucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.buckets {
		if b.ProjectID == projectID && b.Name == name {
			return b, nil
		}
	}
	return nil, nil
}

func (m *memBucketStore) ListBuckets(_ context.Context, projectID string) ([]Bucket, error) {
	return nil, nil
}
func (m *memBucketStore) DeleteBucket(_ context.Context, projectID, name string) error { return nil }
func (m *memBucketStore) SetBucketStatus(_ context.Context, projectID, name, status string) error {
	return nil
}

func (m *memBucketStore) CreateObject(_ context.Context, o *Object) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[o.BucketID]; !ok {
		m.objects[o.BucketID] = map[string]*Object{}
	}
	m.objects[o.BucketID][o.Key] = o
	return nil
}

func (m *memBucketStore) GetObject(_ context.Context, bucketID, key string) (*Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.objects[bucketID][key], nil
}

func (m *memBucketStore) ListObjects(_ context.Context, bucketID, prefix string, limit int, _ string) ([]Object, string, error) {
	return nil, "", nil
}
func (m *memBucketStore) DeleteObject(_ context.Context, bucketID, key string) error { return nil }
func (m *memBucketStore) GetQuotaBytes(_ context.Context, projectID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quotas[projectID], nil
}
func (m *memBucketStore) AddQuotaBytes(_ context.Context, projectID string, delta int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotas[projectID] += delta
	return nil
}

func newTestService(t *testing.T, store BucketStore, tierQuotas map[string]int64) *Service {
	t.Helper()
	r2, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "platform-test",
	})
	if err != nil {
		t.Fatalf("r2 client: %v", err)
	}
	return NewService(store, r2, tierQuotas)
}

func TestStartResumableUpload_BuildsCanonicalKey(t *testing.T) {
	store := newMemBucketStore()
	_ = store.CreateBucket(context.Background(), &Bucket{ID: "b1", ProjectID: "proj1", Name: "media"})
	svc := newTestService(t, store, nil)

	key, err := svc.StartResumableUpload(context.Background(), "proj1", "media", "free",
		UploadURLRequest{Key: "videos/clip.mp4", MimeType: "video/mp4", Size: 1000})
	if err != nil {
		t.Fatalf("StartResumableUpload: %v", err)
	}
	want := "projects/proj1/buckets/media/videos/clip.mp4"
	if key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}

func TestStartResumableUpload_UnknownBucket(t *testing.T) {
	svc := newTestService(t, newMemBucketStore(), nil)
	_, err := svc.StartResumableUpload(context.Background(), "proj1", "ghost", "free",
		UploadURLRequest{Key: "x.bin", Size: 1})
	if err == nil {
		t.Fatal("expected error for unknown bucket")
	}
}

func TestStartResumableUpload_MimeNotAllowed(t *testing.T) {
	store := newMemBucketStore()
	_ = store.CreateBucket(context.Background(), &Bucket{
		ID: "b1", ProjectID: "proj1", Name: "images", AllowedTypes: []string{"image/png"},
	})
	svc := newTestService(t, store, nil)
	_, err := svc.StartResumableUpload(context.Background(), "proj1", "images", "free",
		UploadURLRequest{Key: "a.exe", MimeType: "application/octet-stream", Size: 1})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected mime-not-allowed error, got %v", err)
	}
}

func TestStartResumableUpload_QuotaExceeded(t *testing.T) {
	store := newMemBucketStore()
	_ = store.CreateBucket(context.Background(), &Bucket{ID: "b1", ProjectID: "proj1", Name: "media"})
	svc := newTestService(t, store, map[string]int64{"free": 100})
	_, err := svc.StartResumableUpload(context.Background(), "proj1", "media", "free",
		UploadURLRequest{Key: "big.bin", Size: 200})
	if err == nil || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("expected quota error, got %v", err)
	}
}

func TestTusComposer_NonNilWhenR2Configured(t *testing.T) {
	svc := newTestService(t, newMemBucketStore(), nil)
	if svc.TusComposer() == nil {
		t.Fatal("expected non-nil composer when R2 configured")
	}
}

func TestTusComposer_NilWhenNoR2(t *testing.T) {
	svc := NewService(newMemBucketStore(), nil, nil)
	if svc.TusComposer() != nil {
		t.Fatal("expected nil composer when R2 not configured")
	}
}
