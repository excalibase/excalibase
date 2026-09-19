package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

// fakeObjectStoreForTest is an in-memory blob plane for the storage handler
// tests. The handler must be able to tell "the bytes are gone" from "the
// delete failed", which an unreachable endpoint cannot express: it only ever
// fails. Keys are namespaced "<project>/<bucket>/<key>" so a listing for one
// bucket can never see another's.
type fakeObjectStoreForTest struct {
	mu      sync.Mutex
	objects map[string]fakeStoredObjectForTest
}

// fakeStoredObjectForTest is what the fake plane holds for one key: enough
// for the confirm path to read a size and a type back.
type fakeStoredObjectForTest struct {
	size     int64
	mimeType string
	written  time.Time
}

func newFakeObjectStoreForTest() *fakeObjectStoreForTest {
	return &fakeObjectStoreForTest{objects: map[string]fakeStoredObjectForTest{}}
}

func fakeBlobKey(projectID, bucket, key string) string {
	return projectID + "/" + bucket + "/" + key
}

func (f *fakeObjectStoreForTest) put(projectID, bucketID, key string, size int64, mimeType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[fakeBlobKey(projectID, bucketID, key)] = fakeStoredObjectForTest{
		size: size, mimeType: mimeType, written: time.Now().UTC(),
	}
}

func (f *fakeObjectStoreForTest) HeadObject(_ context.Context, projectID, bucketID, key string) (storagesvc.ObjectStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[fakeBlobKey(projectID, bucketID, key)]
	if !ok {
		return storagesvc.ObjectStat{}, fmt.Errorf("head %q: %w", key, storagesvc.ErrObjectNotFound)
	}
	return storagesvc.ObjectStat{
		Size: obj.size, ContentType: obj.mimeType,
		ETag: "fake-etag", LastModified: obj.written,
	}, nil
}

func (f *fakeObjectStoreForTest) SignedPutURL(_ context.Context, projectID, bucket, key, _ string, _ int64, ttl time.Duration) (string, time.Time, error) {
	return "https://fake.invalid/" + fakeBlobKey(projectID, bucket, key), time.Now().Add(ttl), nil
}

func (f *fakeObjectStoreForTest) SignedGetURL(_ context.Context, projectID, bucket, key string, download bool, ttl time.Duration) (string, time.Time, error) {
	url := "https://fake.invalid/" + fakeBlobKey(projectID, bucket, key)
	if download {
		url += "?response-content-disposition=attachment"
	}
	return url, time.Now().Add(ttl), nil
}

func (f *fakeObjectStoreForTest) PublicURL(projectID, bucket, key string) (string, error) {
	if key == "" {
		return "", errors.New("invalid key")
	}
	return "https://fake.invalid/" + fakeBlobKey(projectID, bucket, key), nil
}

func (f *fakeObjectStoreForTest) DeleteObject(_ context.Context, projectID, bucket, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, fakeBlobKey(projectID, bucket, key))
	return nil
}

func (f *fakeObjectStoreForTest) ListObjects(_ context.Context, projectID, bucketID string, limit int32) ([]storagesvc.StoredObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := projectID + "/" + bucketID + "/"
	out := []storagesvc.StoredObject{}
	for k, obj := range f.objects {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, storagesvc.StoredObject{
				Key:          strings.TrimPrefix(k, prefix),
				Size:         obj.size,
				LastModified: obj.written,
			})
			if limit > 0 && int32(len(out)) >= limit {
				break
			}
		}
	}
	return out, nil
}
