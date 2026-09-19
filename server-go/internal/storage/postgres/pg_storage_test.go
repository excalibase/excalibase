//go:build integration

package postgres

import (
	"context"
	"strconv"
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

	// Delete releases the row and its bytes together.
	removed, err := s.DeleteObjectAndReleaseQuota(ctx, project, bkt.ID, "a.png")
	if err != nil {
		t.Fatalf("DeleteObjectAndReleaseQuota: %v", err)
	}
	if !removed {
		t.Error("delete should report that a row was removed")
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

// A bucket is born active and can be marked deleting; the status survives a
// round-trip so an interrupted cascade is still visible after a restart.
func TestPgStorage_BucketStatusRoundTrips(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const project = "proj-status"
	now := time.Now().UTC()
	if err := s.CreateBucket(ctx, &storagesvc.Bucket{
		ID: "bkt_status", ProjectID: project, Name: "assets",
		Status: storagesvc.BucketStatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	got, _ := s.GetBucket(ctx, project, "assets")
	if got == nil || got.Status != storagesvc.BucketStatusActive {
		t.Fatalf("new bucket should be active: %+v", got)
	}

	if err := s.SetBucketStatus(ctx, project, "assets", storagesvc.BucketStatusDeleting); err != nil {
		t.Fatalf("SetBucketStatus: %v", err)
	}
	got, _ = s.GetBucket(ctx, project, "assets")
	if got == nil || got.Status != storagesvc.BucketStatusDeleting {
		t.Fatalf("bucket should be deleting: %+v", got)
	}
	listed, _ := s.ListBuckets(ctx, project)
	if len(listed) != 1 || listed[0].Status != storagesvc.BucketStatusDeleting {
		t.Errorf("listed status: %+v", listed)
	}
	_ = s.DeleteBucket(ctx, project, "assets")
}

func TestPgStorage_SetBucketStatus_UnknownBucket(t *testing.T) {
	s := testStore(t)
	err := s.SetBucketStatus(context.Background(), "nope", "missing", storagesvc.BucketStatusDeleting)
	if err == nil {
		t.Error("marking a bucket that does not exist must fail")
	}
}

// EXC-404 follow-up, finding 3 — the object-listing prefix is a literal key
// prefix, not a pattern. A caller whose folder name contains % or _ must not
// see its neighbours' objects.
func TestPgStorage_ListObjectsPrefixIsLiteral(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const project = "proj-prefix"
	now := time.Now().UTC()
	if err := s.CreateBucket(ctx, &storagesvc.Bucket{
		ID: "bkt_prefix", ProjectID: project, Name: "files", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	keys := []string{"a%b.txt", "axb.txt", "a_c.txt", "azc.txt"}
	for i, k := range keys {
		if err := s.CreateObject(ctx, &storagesvc.Object{
			ID: "obj_prefix_" + strconv.Itoa(i), BucketID: "bkt_prefix", Key: k,
			Size: 1, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("CreateObject %q: %v", k, err)
		}
	}

	cases := map[string][]string{
		"a%": {"a%b.txt"},
		"a_": {"a_c.txt"},
	}
	for prefix, want := range cases {
		got, _, err := s.ListObjects(ctx, "bkt_prefix", prefix, 100, "")
		if err != nil {
			t.Fatalf("ListObjects %q: %v", prefix, err)
		}
		if len(got) != len(want) || (len(got) > 0 && got[0].Key != want[0]) {
			var keys []string
			for _, o := range got {
				keys = append(keys, o.Key)
			}
			t.Errorf("prefix %q matched %v, want %v", prefix, keys, want)
		}
	}
	_ = s.DeleteBucket(ctx, project, "files")
}

// The row and the bytes it was charged for go together, and a repeated delete
// neither fails nor releases the bytes twice.
func TestPgStorage_DeleteObjectReleasesQuotaExactlyOnce(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	const project = "proj-release"
	now := time.Now().UTC()
	if err := s.CreateBucket(ctx, &storagesvc.Bucket{
		ID: "bkt_release", ProjectID: project, Name: "files", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if err := s.CreateObject(ctx, &storagesvc.Object{
		ID: "obj_release", BucketID: "bkt_release", Key: "a.bin", Size: 300,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	if err := s.AddQuotaBytes(ctx, project, 300); err != nil {
		t.Fatalf("AddQuotaBytes: %v", err)
	}

	removed, err := s.DeleteObjectAndReleaseQuota(ctx, project, "bkt_release", "a.bin")
	if err != nil || !removed {
		t.Fatalf("first delete: removed=%v err=%v", removed, err)
	}
	if used, _ := s.GetQuotaBytes(ctx, project); used != 0 {
		t.Errorf("quota after delete: got %d, want 0", used)
	}

	removed, err = s.DeleteObjectAndReleaseQuota(ctx, project, "bkt_release", "a.bin")
	if err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if removed {
		t.Error("a repeated delete must not claim it removed a row")
	}
	if used, _ := s.GetQuotaBytes(ctx, project); used != 0 {
		t.Errorf("quota after repeat: got %d, want 0", used)
	}
	_ = s.DeleteBucket(ctx, project, "files")
// The storage reaper sweeps every project, so it needs the buckets of all of
// them, not one tenant's.
func TestPgStorage_ListAllBucketsSpansProjects(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, b := range []*storagesvc.Bucket{
		{ID: "bkt_all_a", ProjectID: "proj-all-a", Name: "files", CreatedAt: now, UpdatedAt: now},
		{ID: "bkt_all_b", ProjectID: "proj-all-b", Name: "files", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.CreateBucket(ctx, b); err != nil {
			t.Fatalf("CreateBucket %s: %v", b.ID, err)
		}
	}
	all, err := s.ListAllBuckets(ctx)
	if err != nil {
		t.Fatalf("ListAllBuckets: %v", err)
	}
	seen := map[string]bool{}
	for _, b := range all {
		seen[b.ID] = true
	}
	if !seen["bkt_all_a"] || !seen["bkt_all_b"] {
		t.Errorf("both projects' buckets should be listed, got %d buckets", len(all))
	}
	_ = s.DeleteBucket(ctx, "proj-all-a", "files")
	_ = s.DeleteBucket(ctx, "proj-all-b", "files")
}
