package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/security"
)

const paramGroupsKey = "parameter-groups"


type FileSystemParameterGroupStore struct {
	basePath string
	mu       sync.RWMutex
	cache    map[string]*domain.ParameterGroup
}

func NewFileSystemParameterGroupStore(basePath string) (*FileSystemParameterGroupStore, error) {
	dir := filepath.Join(basePath, paramGroupsKey)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	store := &FileSystemParameterGroupStore{
		basePath: basePath,
		cache:    make(map[string]*domain.ParameterGroup),
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if _, err := security.SafePathComponent(e.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.Base(e.Name())))
		if err != nil {
			continue
		}
		var pg domain.ParameterGroup
		if json.Unmarshal(data, &pg) == nil {
			store.cache[pg.Name] = &pg
		}
	}

	return store, nil
}

func (s *FileSystemParameterGroupStore) Save(pg *domain.ParameterGroup) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(pg, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Join(s.basePath, paramGroupsKey)
	if err := os.WriteFile(filepath.Join(dir, pg.Name+".json"), data, 0644); err != nil {
		return fmt.Errorf("write parameter group: %w", err)
	}

	s.cache[pg.Name] = pg
	return nil
}

func (s *FileSystemParameterGroupStore) FindByName(name string) (*domain.ParameterGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	pg := s.cache[name]
	return pg, nil
}

func (s *FileSystemParameterGroupStore) FindAll() ([]*domain.ParameterGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*domain.ParameterGroup, 0, len(s.cache))
	for _, pg := range s.cache {
		result = append(result, pg)
	}
	return result, nil
}

func (s *FileSystemParameterGroupStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.basePath, paramGroupsKey, name+".json")
	os.Remove(path)
	delete(s.cache, name)
	return nil
}
