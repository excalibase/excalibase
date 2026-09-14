package vault

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// MemoryStore is a process-local VaultStore for tests. It mirrors the
// Postgres store's semantics (nil-on-missing reads, sorted prefix listing,
// empty DeletePrefix rejected) and survives Close so a second Vault handle on
// the same store behaves like a restart.
type MemoryStore struct {
	mu      sync.RWMutex
	barrier []byte
	meta    []byte
	secrets map[string][]byte
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{secrets: map[string][]byte{}}
}

func (s *MemoryStore) GetBarrier() ([]byte, []byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneBytes(s.barrier), cloneBytes(s.meta), nil
}

func (s *MemoryStore) PutBarrier(encryptedBarrier []byte, meta []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.barrier = cloneBytes(encryptedBarrier)
	s.meta = cloneBytes(meta)
	return nil
}

func (s *MemoryStore) GetSecret(path string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneBytes(s.secrets[path]), nil
}

func (s *MemoryStore) PutSecret(path string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[path] = cloneBytes(data)
	return nil
}

func (s *MemoryStore) DeleteSecret(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets, path)
	return nil
}

func (s *MemoryStore) DeletePrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, fmt.Errorf("DeletePrefix: empty prefix not allowed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for path := range s.secrets {
		if strings.HasPrefix(path, prefix) {
			delete(s.secrets, path)
			deleted++
		}
	}
	return deleted, nil
}

func (s *MemoryStore) ListSecrets(prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var paths []string
	for path := range s.secrets {
		if strings.HasPrefix(path, prefix) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (s *MemoryStore) Close() error {
	return nil
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
