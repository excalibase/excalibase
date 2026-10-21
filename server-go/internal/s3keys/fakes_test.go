package s3keys

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type fakeStore struct {
	mu      sync.Mutex
	keys    map[string]*Key
	touched map[string]time.Time
	failOn  string
}

func newFakeStore() *fakeStore {
	return &fakeStore{keys: map[string]*Key{}, touched: map[string]time.Time{}}
}

var errStoreDown = errors.New("platform database is down")

func (f *fakeStore) fail(op string) error {
	if f.failOn == op {
		return errStoreDown
	}
	return nil
}

func (f *fakeStore) Create(_ context.Context, key *Key, maxPerProject int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("create"); err != nil {
		return err
	}
	held := 0
	for _, k := range f.keys {
		if k.ProjectID == key.ProjectID {
			held++
		}
	}
	if held >= maxPerProject {
		return ErrLimitReached
	}
	copied := *key
	f.keys[key.ID] = &copied
	return nil
}

func (f *fakeStore) List(_ context.Context, projectID string) ([]*Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("list"); err != nil {
		return nil, err
	}
	out := []*Key{}
	for _, k := range f.keys {
		if k.ProjectID == projectID {
			copied := *k
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) FindByAccessKeyID(_ context.Context, accessKeyID string) (*Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("find"); err != nil {
		return nil, err
	}
	for _, k := range f.keys {
		if k.AccessKeyID == accessKeyID {
			copied := *k
			return &copied, nil
		}
	}
	return nil, nil
}

func (f *fakeStore) Delete(_ context.Context, projectID, id string) (*Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail("delete"); err != nil {
		return nil, err
	}
	k, ok := f.keys[id]
	if !ok || k.ProjectID != projectID {
		return nil, nil
	}
	delete(f.keys, id)
	return k, nil
}

func (f *fakeStore) DeleteProject(_ context.Context, projectID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	removed := 0
	for id, k := range f.keys {
		if k.ProjectID == projectID {
			delete(f.keys, id)
			removed++
		}
	}
	return removed, nil
}

func (f *fakeStore) TouchLastUsed(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched[id] = at
	return nil
}

type fakeSecrets struct {
	mu      sync.Mutex
	secrets map[string]string
	gets    int
	err     error
}

func newFakeSecrets() *fakeSecrets { return &fakeSecrets{secrets: map[string]string{}} }

func (f *fakeSecrets) Put(projectID, accessKeyID, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.secrets[projectID+"/"+accessKeyID] = secret
	return nil
}

func (f *fakeSecrets) Get(projectID, accessKeyID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if f.err != nil {
		return "", f.err
	}
	secret, ok := f.secrets[projectID+"/"+accessKeyID]
	if !ok {
		return "", ErrSecretMissing
	}
	return secret, nil
}

func (f *fakeSecrets) Delete(projectID, accessKeyID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	delete(f.secrets, projectID+"/"+accessKeyID)
	return nil
}

type fakeStanding struct {
	mu   sync.Mutex
	lost map[string]bool
	err  error
}

func (f *fakeStanding) CanUse(_ context.Context, userID, projectID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	return !f.lost[userID+"@"+projectID], nil
}

type fakeBuckets map[string]bool

func (f fakeBuckets) BucketExists(_ context.Context, projectID, name string) (bool, error) {
	return f[projectID+"/"+name], nil
}
