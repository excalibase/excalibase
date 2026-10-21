//go:build integration

package s3keys_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/s3keys"
	"github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
)

func seedUser(t *testing.T, ctx context.Context, store interface {
	CreateUser(context.Context, *domain.User) error
}, id string) {
	t.Helper()
	if err := store.CreateUser(ctx, &domain.User{ID: id, Username: id, Email: id + "@example.test",
		PasswordHash: "x", Role: "user", Active: true}); err != nil {
		t.Fatalf("create user: %v", err)
	}
}

func TestPostgresStore_RoundTrip(t *testing.T) {
	platform := pgstore.New(t)
	ctx := context.Background()
	seedUser(t, ctx, platform, "s3-dev")
	store := s3keys.NewPostgresStore(platform.DB())

	created := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	expires := created.Add(24 * time.Hour)
	key := &s3keys.Key{ID: "s3k_1", ProjectID: "proj-a", Name: "nas", AccessKeyID: "EXS3AAAAAAAAAAAAAAAA",
		Buckets: []string{"backups", "photos"}, Permissions: s3keys.Permissions{Read: true, Delete: true},
		CreatedBy: "s3-dev", CreatedAt: created, ExpiresAt: &expires}
	if err := store.Create(ctx, key, 5); err != nil {
		t.Fatal(err)
	}
	found, err := store.FindByAccessKeyID(ctx, key.AccessKeyID)
	if err != nil || found == nil {
		t.Fatalf("find: %+v %v", found, err)
	}
	if found.Name != "nas" || len(found.Buckets) != 2 || !found.Read || found.Write || !found.Delete ||
		!found.CreatedAt.Equal(created) || found.ExpiresAt == nil || !found.ExpiresAt.Equal(expires) || found.LastUsedAt != nil {
		t.Fatalf("found = %+v", found)
	}
	if missing, err := store.FindByAccessKeyID(ctx, "EXS3BBBBBBBBBBBBBBBB"); err != nil || missing != nil {
		t.Fatalf("missing = %+v %v", missing, err)
	}

	used := created.Add(time.Hour)
	if err := store.TouchLastUsed(ctx, key.ID, used); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(ctx, "proj-a")
	if err != nil || len(listed) != 1 || listed[0].LastUsedAt == nil || !listed[0].LastUsedAt.Equal(used) {
		t.Fatalf("listed = %+v %v", listed, err)
	}
	if other, _ := store.List(ctx, "proj-b"); len(other) != 0 {
		t.Fatal("another project lists nothing")
	}

	if gone, err := store.Delete(ctx, "proj-b", key.ID); err != nil || gone != nil {
		t.Fatalf("another project cannot delete it: %+v %v", gone, err)
	}
	gone, err := store.Delete(ctx, "proj-a", key.ID)
	if err != nil || gone == nil || gone.AccessKeyID != key.AccessKeyID {
		t.Fatalf("delete = %+v %v", gone, err)
	}
	if found, _ := store.FindByAccessKeyID(ctx, key.AccessKeyID); found != nil {
		t.Fatal("deleted")
	}
}

func TestPostgresStore_AllBucketsIsAnEmptyList(t *testing.T) {
	platform := pgstore.New(t)
	ctx := context.Background()
	seedUser(t, ctx, platform, "s3-dev")
	store := s3keys.NewPostgresStore(platform.DB())
	key := &s3keys.Key{ID: "s3k_all", ProjectID: "proj-a", Name: "all", AccessKeyID: "EXS3CCCCCCCCCCCCCCCC",
		Buckets: []string{}, Permissions: s3keys.Permissions{Read: true}, CreatedBy: "s3-dev", CreatedAt: time.Now().UTC()}
	if err := store.Create(ctx, key, 5); err != nil {
		t.Fatal(err)
	}
	found, _ := store.FindByAccessKeyID(ctx, key.AccessKeyID)
	if found.Buckets == nil || len(found.Buckets) != 0 || !found.Lists("anything") {
		t.Fatalf("buckets = %#v", found.Buckets)
	}
}

func TestPostgresStore_LimitHoldsUnderConcurrentCreates(t *testing.T) {
	platform := pgstore.New(t)
	ctx := context.Background()
	seedUser(t, ctx, platform, "s3-dev")
	store := s3keys.NewPostgresStore(platform.DB())
	const limit = 3
	var wg sync.WaitGroup
	results := make([]error, 10)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = store.Create(ctx, &s3keys.Key{ID: "s3k_c" + string(rune('a'+i)), ProjectID: "proj-c", Name: "k",
				AccessKeyID: "EXS3DDDDDDDDDDDDDDD" + string(rune('A'+i)), Buckets: []string{},
				Permissions: s3keys.Permissions{Read: true}, CreatedBy: "s3-dev", CreatedAt: time.Now().UTC()}, limit)
		}(i)
	}
	wg.Wait()
	created := 0
	for _, err := range results {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, s3keys.ErrLimitReached):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if created != limit {
		t.Fatalf("created %d, want exactly %d", created, limit)
	}
	removed, err := store.DeleteProject(ctx, "proj-c")
	if err != nil || removed != limit {
		t.Fatalf("removed %d, %v", removed, err)
	}
}

func TestPostgresStore_DeletingTheCreatorRemovesTheirKeys(t *testing.T) {
	platform := pgstore.New(t)
	ctx := context.Background()
	seedUser(t, ctx, platform, "s3-gone")
	store := s3keys.NewPostgresStore(platform.DB())
	key := &s3keys.Key{ID: "s3k_gone", ProjectID: "proj-a", Name: "k", AccessKeyID: "EXS3EEEEEEEEEEEEEEEE",
		Buckets: []string{}, Permissions: s3keys.Permissions{Read: true}, CreatedBy: "s3-gone", CreatedAt: time.Now().UTC()}
	if err := store.Create(ctx, key, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.DB().ExecContext(ctx, `DELETE FROM users WHERE id = 's3-gone'`); err != nil {
		t.Fatal(err)
	}
	if found, _ := store.FindByAccessKeyID(ctx, key.AccessKeyID); found != nil {
		t.Fatal("a key never outlives its creator's account")
	}
}
