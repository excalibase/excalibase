package storagesvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// fakeObjectStore is an in-memory blob plane. It records the keys it holds so
// a test can assert what survived a delete, and can be told to fail a
// specific operation — the failure modes (a store that rejects deletes, a
// store that cannot be listed) are exactly what EXC-404 is about and a live
// endpoint cannot produce them on demand.
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string]fakeStoredObject // "<project>/<bucketID>/<key>"

	deleteErr error
	listErr   error
	headErr   error
	signErr   error
	copyErr   error
	// ignoreDeletes models a store that accepts a delete and keeps the
	// object anyway — a fault a purge must notice rather than spin on.
	ignoreDeletes bool
}

// fakeStoredObject is what the fake plane holds for one key: enough for the
// confirm path to read a size and a type back.
type fakeStoredObject struct {
	size         int64
	mimeType     string
	lastModified time.Time
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string]fakeStoredObject{}}
}

// fakeStoreKey mirrors the real key layout, so a prefix a test asserts on is
// the prefix production code would build — including the project-wide one a
// teardown works from.
func fakeStoreKey(projectID, bucketID, key string) string {
	return "projects/" + projectID + "/buckets/" + bucketID + "/" + key
}

// put makes the plane hold key with the given size and type, written now.
func (f *fakeObjectStore) put(projectID, bucketID, key string, size int64, mimeType string) {
	f.putAt(projectID, bucketID, key, size, mimeType, time.Now().UTC())
}

// putAt is put with an explicit write time, for ageing an object past the
// reaper's grace period.
func (f *fakeObjectStore) putAt(projectID, bucketID, key string, size int64, mimeType string, written time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[fakeStoreKey(projectID, bucketID, key)] = fakeStoredObject{
		size: size, mimeType: mimeType, lastModified: written,
	}
}

func (f *fakeObjectStore) has(projectID, bucketID, key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[fakeStoreKey(projectID, bucketID, key)]
	return ok
}

func (f *fakeObjectStore) SignedPutURL(_ context.Context, projectID, bucketID, key, _ string, _ int64, ttl time.Duration) (string, time.Time, error) {
	if f.signErr != nil {
		return "", time.Time{}, f.signErr
	}
	return "https://fake/" + fakeStoreKey(projectID, bucketID, key), time.Now().Add(ttl), nil
}

func (f *fakeObjectStore) HeadObject(_ context.Context, projectID, bucketID, key string) (ObjectStat, error) {
	if f.headErr != nil {
		return ObjectStat{}, f.headErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[fakeStoreKey(projectID, bucketID, key)]
	if !ok {
		return ObjectStat{}, fmt.Errorf("head %q: %w", key, ErrObjectNotFound)
	}
	return ObjectStat{
		Size: obj.size, ContentType: obj.mimeType,
		ETag: "fake-etag", LastModified: obj.lastModified,
	}, nil
}

func (f *fakeObjectStore) SignedGetURL(_ context.Context, projectID, bucketID, key string, download bool, ttl time.Duration) (string, time.Time, error) {
	if f.signErr != nil {
		return "", time.Time{}, f.signErr
	}
	url := "https://fake/" + fakeStoreKey(projectID, bucketID, key)
	if download {
		url += "?response-content-disposition=attachment"
	}
	return url, time.Now().Add(ttl), nil
}

func (f *fakeObjectStore) PublicURL(projectID, bucketID, key string) (string, error) {
	if key == "" {
		return "", errors.New("invalid key")
	}
	return "https://fake/" + fakeStoreKey(projectID, bucketID, key), nil
}

// ListKeysWithPrefix is the one listing the fake implements; the bucket- and
// staging-scoped views below build their prefixes and call through it.
func (f *fakeObjectStore) ListKeysWithPrefix(_ context.Context, prefix string, limit int32) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []string{}
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
			if limit > 0 && int32(len(out)) >= limit {
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// DeleteKey refuses a key outside the namespace the caller named, the way the
// real client does.
func (f *fakeObjectStore) DeleteKey(_ context.Context, prefix, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if prefix == "" || !strings.HasPrefix(key, prefix) {
		return errors.New("key outside the caller's prefix")
	}
	if f.ignoreDeletes {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, key)
	return nil
}

func (f *fakeObjectStore) DeleteObject(_ context.Context, projectID, bucketID, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, fakeStoreKey(projectID, bucketID, key))
	return nil
}

func (f *fakeObjectStore) CopyObject(_ context.Context, projectID, bucketID, sourceKey, destinationKey string) error {
	if f.copyErr != nil {
		return f.copyErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	source, ok := f.objects[fakeStoreKey(projectID, bucketID, sourceKey)]
	if !ok {
		return fmt.Errorf("copy %q: %w", sourceKey, ErrObjectNotFound)
	}
	f.objects[fakeStoreKey(projectID, bucketID, destinationKey)] = source
	return nil
}

// ListStagedUploads sees the staging namespace and nothing else, the way the
// real client's fixed prefix does.
func (f *fakeObjectStore) ListStagedUploads(_ context.Context, projectID, bucketID string, limit int32) ([]StagedUpload, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := fakeStoreKey(projectID, bucketID, stagingPrefix)
	out := []StagedUpload{}
	for k, obj := range f.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, StagedUpload{
				UploadID:     strings.TrimPrefix(k, prefix),
				LastModified: obj.lastModified,
			})
			if limit > 0 && int32(len(out)) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeObjectStore) DeleteStagingObject(ctx context.Context, projectID, bucketID, uploadID string) error {
	return f.DeleteObject(ctx, projectID, bucketID, stagingObjectKey(uploadID))
}

// ListObjects reproduces the prefix semantics the real client must have:
// only keys whose "<project>/<bucketID>/" prefix matches exactly, so id
// "bkt_1" never sees "bkt_12".
func (f *fakeObjectStore) ListObjects(_ context.Context, projectID, bucketID string, limit int32) ([]StoredObject, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := fakeStoreKey(projectID, bucketID, "")
	out := []StoredObject{}
	for k, obj := range f.objects {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, StoredObject{
				Key:          strings.TrimPrefix(k, prefix),
				Size:         obj.size,
				LastModified: obj.lastModified,
			})
			if limit > 0 && int32(len(out)) >= limit {
				break
			}
		}
	}
	return out, nil
}
