package storagesvc

import (
	"context"
	"errors"
	"sync"
	"time"
)

// fakeObjectStore is an in-memory blob plane. It records the keys it holds so
// a test can assert what survived a delete, and can be told to fail a
// specific operation — the failure modes (a store that rejects deletes, a
// store that cannot be listed) are exactly what EXC-404 is about and a live
// endpoint cannot produce them on demand.
type fakeObjectStore struct {
	mu   sync.Mutex
	keys map[string]bool // "<project>/<bucket>/<key>"

	deleteErr error
	listErr   error
	signErr   error
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{keys: map[string]bool{}}
}

func fakeStoreKey(projectID, bucket, key string) string {
	return projectID + "/" + bucket + "/" + key
}

func (f *fakeObjectStore) put(projectID, bucket, key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[fakeStoreKey(projectID, bucket, key)] = true
}

func (f *fakeObjectStore) has(projectID, bucket, key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[fakeStoreKey(projectID, bucket, key)]
}

func (f *fakeObjectStore) SignedPutURL(_ context.Context, projectID, bucket, key, _ string, ttl time.Duration) (string, time.Time, error) {
	if f.signErr != nil {
		return "", time.Time{}, f.signErr
	}
	return "https://fake/" + fakeStoreKey(projectID, bucket, key), time.Now().Add(ttl), nil
}

func (f *fakeObjectStore) SignedGetURL(_ context.Context, projectID, bucket, key string, ttl time.Duration) (string, time.Time, error) {
	if f.signErr != nil {
		return "", time.Time{}, f.signErr
	}
	return "https://fake/" + fakeStoreKey(projectID, bucket, key), time.Now().Add(ttl), nil
}

func (f *fakeObjectStore) PublicURL(projectID, bucket, key string) (string, error) {
	if key == "" {
		return "", errors.New("invalid key")
	}
	return "https://fake/" + fakeStoreKey(projectID, bucket, key), nil
}

func (f *fakeObjectStore) DeleteObject(_ context.Context, projectID, bucket, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.keys, fakeStoreKey(projectID, bucket, key))
	return nil
}

// ListObjectKeys reproduces the prefix semantics the real client must have:
// only keys whose "<project>/<bucket>/" prefix matches exactly, so bucket
// "assets" never sees "assets2".
func (f *fakeObjectStore) ListObjectKeys(_ context.Context, projectID, bucket string, limit int32) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
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
