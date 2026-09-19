package handler

import (
	"context"
	"errors"
	"sync"
	"time"
)

// fakeObjectStoreForTest is an in-memory blob plane for the storage handler
// tests. The handler must be able to tell "the bytes are gone" from "the
// delete failed", which an unreachable endpoint cannot express: it only ever
// fails. Keys are namespaced "<project>/<bucket>/<key>" so a listing for one
// bucket can never see another's.
type fakeObjectStoreForTest struct {
	mu   sync.Mutex
	keys map[string]bool
}

func newFakeObjectStoreForTest() *fakeObjectStoreForTest {
	return &fakeObjectStoreForTest{keys: map[string]bool{}}
}

func fakeBlobKey(projectID, bucket, key string) string {
	return projectID + "/" + bucket + "/" + key
}

func (f *fakeObjectStoreForTest) put(projectID, bucket, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[fakeBlobKey(projectID, bucket, key)] = true
}

func (f *fakeObjectStoreForTest) SignedPutURL(_ context.Context, projectID, bucket, key, _ string, ttl time.Duration) (string, time.Time, error) {
	return "https://fake.invalid/" + fakeBlobKey(projectID, bucket, key), time.Now().Add(ttl), nil
}

func (f *fakeObjectStoreForTest) SignedGetURL(_ context.Context, projectID, bucket, key string, ttl time.Duration) (string, time.Time, error) {
	return "https://fake.invalid/" + fakeBlobKey(projectID, bucket, key), time.Now().Add(ttl), nil
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
	delete(f.keys, fakeBlobKey(projectID, bucket, key))
	return nil
}

func (f *fakeObjectStoreForTest) ListObjectKeys(_ context.Context, projectID, bucket string, limit int32) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prefix := projectID + "/" + bucket + "/"
	out := []string{}
	for k := range f.keys {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
			if limit > 0 && int32(len(out)) >= limit {
				break
			}
		}
	}
	return out, nil
}
