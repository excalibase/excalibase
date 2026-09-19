package storagesvc

import (
	"context"
	"strings"
	"sync"
	"testing"
)

const (
	testProjX = "proj-x"
	testR2URL = "https://acct.r2.cloudflarestorage.com"
)

// memStore is an in-memory BucketStore for tests. Mirrors the contract
// the SQL implementation has to satisfy — keeping it nearby so changes
// to the interface flag here too.
type memStore struct {
	mu      sync.Mutex
	buckets map[string]*Bucket            // id → bucket
	objects map[string]map[string]*Object // bucketID → key → object
	quotas  map[string]int64
}

func newMemStore() *memStore {
	return &memStore{
		buckets: map[string]*Bucket{},
		objects: map[string]map[string]*Object{},
		quotas:  map[string]int64{},
	}
}

func (m *memStore) CreateBucket(_ context.Context, b *Bucket) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.buckets {
		if existing.ProjectID == b.ProjectID && existing.Name == b.Name {
			return errAlreadyExists
		}
	}
	m.buckets[b.ID] = b
	m.objects[b.ID] = map[string]*Object{}
	return nil
}

func (m *memStore) GetBucket(_ context.Context, projectID, name string) (*Bucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.buckets {
		if b.ProjectID == projectID && b.Name == name {
			return b, nil
		}
	}
	return nil, nil
}

func (m *memStore) ListBuckets(_ context.Context, projectID string) ([]Bucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Bucket{}
	for _, b := range m.buckets {
		if b.ProjectID == projectID {
			out = append(out, *b)
		}
	}
	return out, nil
}

func (m *memStore) ListAllBuckets(_ context.Context) ([]Bucket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Bucket{}
	for _, b := range m.buckets {
		out = append(out, *b)
	}
	return out, nil
}

func (m *memStore) DeleteBucket(_ context.Context, projectID, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, b := range m.buckets {
		if b.ProjectID == projectID && b.Name == name {
			delete(m.buckets, id)
			delete(m.objects, id)
			return nil
		}
	}
	return errNotFound
}

func (m *memStore) SetBucketStatus(_ context.Context, projectID, name, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.buckets {
		if b.ProjectID == projectID && b.Name == name {
			b.Status = status
			return nil
		}
	}
	return errNotFound
}

func (m *memStore) CreateObject(_ context.Context, o *Object) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objects[o.BucketID]; !ok {
		m.objects[o.BucketID] = map[string]*Object{}
	}
	m.objects[o.BucketID][o.Key] = o
	return nil
}

func (m *memStore) GetObject(_ context.Context, bucketID, key string) (*Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.objects[bucketID]; ok {
		return b[key], nil
	}
	return nil, nil
}

func (m *memStore) ListObjects(_ context.Context, bucketID, prefix string, limit int, _ string) ([]Object, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Object{}
	for _, o := range m.objects[bucketID] {
		if prefix == "" || strings.HasPrefix(o.Key, prefix) {
			out = append(out, *o)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, "", nil
}

// DeleteObjectAndReleaseQuota mirrors the SQL contract: the row goes and its
// own size is released, together or not at all.
func (m *memStore) DeleteObjectAndReleaseQuota(_ context.Context, projectID, bucketID, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[bucketID][key]
	if !ok {
		return false, nil
	}
	delete(m.objects[bucketID], key)
	m.quotas[projectID] -= obj.Size
	if m.quotas[projectID] < 0 {
		m.quotas[projectID] = 0
	}
	return true, nil
}

func (m *memStore) GetQuotaBytes(_ context.Context, projectID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.quotas[projectID], nil
}

func (m *memStore) AddQuotaBytes(_ context.Context, projectID string, delta int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quotas[projectID] += delta
	return nil
}

// errAlreadyExists / errNotFound — sentinel values used by the in-memory
// store. The SQL impls return their own errors with the same semantics.
var errAlreadyExists = stringError("already exists")
var errNotFound = stringError("not found")

type stringError string

func (e stringError) Error() string { return string(e) }

// --- actual tests ---

func TestValidateBucketName(t *testing.T) {
	bad := []string{
		"",                      // too short
		"ab",                    // too short
		strings.Repeat("a", 64), // too long
		"-leading",
		"trailing-",
		"upper-Case",
		"with space",
		"with_underscore",
		"with.dot",
	}
	for _, n := range bad {
		if err := validateBucketName(n); err == nil {
			t.Errorf("%q should fail", n)
		}
	}
	good := []string{"avatars", "user-files", "abc-123", "a-b-c"}
	for _, n := range good {
		if err := validateBucketName(n); err != nil {
			t.Errorf("%q should pass: %v", n, err)
		}
	}
}

func TestService_CreateBucket_Persists(t *testing.T) {
	svc := NewService(newMemStore(), nil, nil)
	b, err := svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{
		Name:   "avatars",
		Public: true,
	})
	if err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if b.Name != "avatars" || !b.Public || b.ProjectID != testProjX {
		t.Errorf("unexpected bucket: %+v", b)
	}
	if !strings.HasPrefix(b.ID, "bkt_") {
		t.Errorf("id prefix: %s", b.ID)
	}
}

func TestService_CreateBucket_RejectsDuplicate(t *testing.T) {
	svc := NewService(newMemStore(), nil, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "avatars"})
	_, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "avatars"})
	if err == nil {
		t.Error("duplicate bucket should fail")
	}
}

// TestService_QuotaEnforcement — when tier quotas are configured and the
// upload size hint would exceed the cap, SignUploadURL must refuse rather
// than minting a URL the user could PUT bytes through anyway.
func TestService_QuotaEnforcement(t *testing.T) {
	store := newMemStore()
	r2, _ := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: testR2URL,
		Bucket:   testPlatformBucket,
	})
	svc := NewService(store, r2, map[string]int64{
		"free": 1024 * 1024, // 1 MiB
	})
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "files"})

	// Pre-fill quota close to the cap.
	_ = store.AddQuotaBytes(ctx, testProjX, 1024*1024-100)

	// 200-byte upload would push over → reject.
	_, err := svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "big.txt", MimeType: "text/plain", Size: 200,
	})
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("expected quota error, got %v", err)
	}

	// 50-byte upload fits → ok.
	_, err = svc.SignUploadURL(ctx, testProjX, "files", "FREE", UploadURLRequest{
		Key: "small.txt", MimeType: "text/plain", Size: 50,
	})
	if err != nil {
		t.Errorf("under-quota upload rejected: %v", err)
	}
}

// TestService_BucketSizeLimit pins the per-bucket file-size cap as
// a separate enforcement layer from the project quota. Useful for
// e.g. avatars bucket capping at 2 MB even when the project has 50 GB free.
func TestService_BucketSizeLimit(t *testing.T) {
	store := newMemStore()
	r2, _ := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: testR2URL,
		Bucket:   testPlatformBucket,
	})
	svc := NewService(store, r2, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{
		Name:          "avatars",
		FileSizeLimit: 2 * 1024 * 1024,
	})

	_, err := svc.SignUploadURL(ctx, testProjX, "avatars", "FREE", UploadURLRequest{
		Key: "big.png", MimeType: "image/png", Size: 5 * 1024 * 1024,
	})
	if err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Errorf("expected size-limit error, got %v", err)
	}
}

func TestService_MimeAllowlist(t *testing.T) {
	store := newMemStore()
	r2, _ := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: testR2URL,
		Bucket:   testPlatformBucket,
	})
	svc := NewService(store, r2, nil)
	ctx := context.Background()
	_, _ = svc.CreateBucket(ctx, testProjX, CreateBucketRequest{
		Name:             "images",
		AllowedMimeTypes: []string{"image/png", "image/jpeg"},
	})

	// Disallowed mime → reject.
	_, err := svc.SignUploadURL(ctx, testProjX, "images", "FREE", UploadURLRequest{
		Key: "doc.pdf", MimeType: "application/pdf", Size: 100,
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("expected mime error, got %v", err)
	}

	// Allowed mime → ok.
	_, err = svc.SignUploadURL(ctx, testProjX, "images", "FREE", UploadURLRequest{
		Key: "pic.png", MimeType: "image/png", Size: 100,
	})
	if err != nil {
		t.Errorf("png upload rejected: %v", err)
	}
}
