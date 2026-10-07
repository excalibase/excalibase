package storagesvc

import (
	"context"
	"errors"
	"testing"
)

func TestCreateBucket_KeepsItsAccessRules(t *testing.T) {
	svc := NewServiceWithObjectStore(newMemStore(), newFakeObjectStore(), nil)
	access := BucketAccess{"authenticated": {Read: ScopeOwn, Write: ScopeOwn}}
	created, err := svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{Name: "avatars", Access: access})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Access["authenticated"].Write != ScopeOwn {
		t.Fatalf("access not kept: %+v", created.Access)
	}
	got, err := svc.Bucket(context.Background(), testProjX, "avatars")
	if err != nil || got.Access["authenticated"].Read != ScopeOwn {
		t.Fatalf("bucket lookup lost the rules: %+v, %v", got, err)
	}
}

func TestCreateBucket_RefusesBadAccessRules(t *testing.T) {
	svc := NewServiceWithObjectStore(newMemStore(), newFakeObjectStore(), nil)
	_, err := svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{
		Name: "avatars", Access: BucketAccess{"authenticated": {Read: "everyone"}},
	})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("want validation error, got %v", err)
	}
}

func TestUpdateBucketAccess_ReplacesTheRules(t *testing.T) {
	svc := NewServiceWithObjectStore(newMemStore(), newFakeObjectStore(), nil)
	ctx := context.Background()
	if _, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{
		Name: "avatars", Access: BucketAccess{"anon": {Read: ScopeAll}},
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := svc.UpdateBucketAccess(ctx, testProjX, "avatars", BucketAccess{"authenticated": {Write: ScopeOwn}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, stale := updated.Access["anon"]; stale {
		t.Error("the old rule survived a replace")
	}
	got, _ := svc.Bucket(ctx, testProjX, "avatars")
	if got.Access["authenticated"].Write != ScopeOwn {
		t.Errorf("stored rules: %+v", got.Access)
	}
}

// failingCatalogue is a catalogue whose reads and access writes fail.
type failingCatalogue struct{ *memStore }

func (failingCatalogue) GetBucket(context.Context, string, string) (*Bucket, error) {
	return nil, errors.New("catalogue down")
}

func (failingCatalogue) UpdateBucketAccess(context.Context, string, string, BucketAccess) (bool, error) {
	return false, errors.New("catalogue down")
}

func TestBucketAccess_CatalogueFailuresAreReported(t *testing.T) {
	svc := NewServiceWithObjectStore(failingCatalogue{newMemStore()}, newFakeObjectStore(), nil)
	ctx := context.Background()
	if _, err := svc.Bucket(ctx, testProjX, "avatars"); err == nil || errors.Is(err, ErrBucketNotFound) {
		t.Errorf("bucket lookup: %v, want the store failure", err)
	}
	if _, err := svc.UpdateBucketAccess(ctx, testProjX, "avatars", nil); err == nil || errors.Is(err, ErrBucketNotFound) {
		t.Errorf("update access: %v, want the store failure", err)
	}
}

func TestUpdateBucketAccess_UnknownBucketAndBadRules(t *testing.T) {
	svc := NewServiceWithObjectStore(newMemStore(), newFakeObjectStore(), nil)
	ctx := context.Background()
	if _, err := svc.UpdateBucketAccess(ctx, testProjX, "nope", BucketAccess{}); !errors.Is(err, ErrBucketNotFound) {
		t.Errorf("unknown bucket: %v", err)
	}
	if _, err := svc.CreateBucket(ctx, testProjX, CreateBucketRequest{Name: "avatars"}); err != nil {
		t.Fatal(err)
	}
	var invalid *ValidationError
	if _, err := svc.UpdateBucketAccess(ctx, testProjX, "avatars", BucketAccess{"Bad-Role": {}}); !errors.As(err, &invalid) {
		t.Errorf("bad rules: %v", err)
	}
}

func TestBucket_UnknownIsNotFound(t *testing.T) {
	svc := NewServiceWithObjectStore(newMemStore(), newFakeObjectStore(), nil)
	if _, err := svc.Bucket(context.Background(), testProjX, "nope"); !errors.Is(err, ErrBucketNotFound) {
		t.Errorf("got %v", err)
	}
}
