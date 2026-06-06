//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

func TestPgStorage_BucketAndObjectLifecycle(t *testing.T) { //NOSONAR sequential lifecycle scenario; splitting would obscure the flow
	s := testStore(t)
	ctx := context.Background()
	const project = "proj-store"

	// Create a bucket.
	now := time.Now().UTC()
	bkt := &storagesvc.Bucket{
		ID:           "bkt_1",
		ProjectID:    project,
		Name:         "files",
		Public:       true,
		FileSize:     1024,
		AllowedTypes: []string{"image/png"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.CreateBucket(ctx, bkt); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}

	// GetBucket round-trips.
	got, err := s.GetBucket(ctx, project, "files")
	if err != nil {
		t.Fatalf("GetBucket: %v", err)
	}
	if got == nil || got.Name != "files" || !got.Public {
		t.Fatalf("GetBucket mismatch: %+v", got)
	}

	// ListBuckets scoped to project.
	list, err := s.ListBuckets(ctx, project)
	if err != nil {
		t.Fatalf("ListBuckets: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("ListBuckets: got %d, want 1", len(list))
	}

	// Create objects + quota.
	for _, k := range []string{"a.png", "b.png", "sub/c.png"} {
		obj := &storagesvc.Object{
			ID: "obj_" + k, BucketID: bkt.ID, Key: k, Size: 100,
			MimeType: "image/png", OwnerID: "u1", CreatedAt: now, UpdatedAt: now,
		}
		if err := s.CreateObject(ctx, obj); err != nil {
			t.Fatalf("CreateObject %s: %v", k, err)
		}
	}
	if err := s.AddQuotaBytes(ctx, project, 300); err != nil {
		t.Fatalf("AddQuotaBytes: %v", err)
	}
	if used, _ := s.GetQuotaBytes(ctx, project); used != 300 {
		t.Errorf("GetQuotaBytes: got %d, want 300", used)
	}

	// GetObject.
	one, err := s.GetObject(ctx, bkt.ID, "a.png")
	if err != nil || one == nil || one.Key != "a.png" {
		t.Fatalf("GetObject: %v / %+v", err, one)
	}

	// ListObjects all + prefix.
	all, _, err := s.ListObjects(ctx, bkt.ID, "", 100, "")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("ListObjects all: got %d, want 3", len(all))
	}
	sub, _, err := s.ListObjects(ctx, bkt.ID, "sub/", 100, "")
	if err != nil {
		t.Fatalf("ListObjects prefix: %v", err)
	}
	if len(sub) != 1 {
		t.Errorf("ListObjects prefix: got %d, want 1", len(sub))
	}

	// DeleteObject.
	if err := s.DeleteObject(ctx, bkt.ID, "a.png"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if gone, _ := s.GetObject(ctx, bkt.ID, "a.png"); gone != nil {
		t.Error("object should be gone after delete")
	}

	// DeleteBucket.
	if err := s.DeleteBucket(ctx, project, "files"); err != nil {
		t.Fatalf("DeleteBucket: %v", err)
	}
	if after, _ := s.GetBucket(ctx, project, "files"); after != nil {
		t.Error("bucket should be gone after delete")
	}
}

func TestPgStorage_GetBucket_NotFound(t *testing.T) {
	s := testStore(t)
	got, err := s.GetBucket(context.Background(), "nope", "missing")
	if err != nil {
		t.Fatalf("GetBucket err: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil bucket, got %+v", got)
	}
}

func TestPgStorage_QuotaDeltaAccumulates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const project = "proj-quota"
	_ = s.AddQuotaBytes(ctx, project, 500)
	_ = s.AddQuotaBytes(ctx, project, -200)
	if used, _ := s.GetQuotaBytes(ctx, project); used != 300 {
		t.Errorf("quota accumulation: got %d, want 300", used)
	}
}
