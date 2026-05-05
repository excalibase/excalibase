package edgefn

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/security"
)

// FunctionStore persists per-project Functions to the local filesystem.
// Layout: {basePath}/functions/{projectId}/{id}.json
type FunctionStore struct {
	basePath string
	mu       sync.RWMutex
	fns      map[string]*Function // key = projectId + "/" + id
}

func NewFunctionStore(basePath string) *FunctionStore {
	dir := filepath.Join(basePath, "functions")
	if err := os.MkdirAll(dir, 0750); err != nil {
		log.Printf("WARN: create functions dir: %v", err)
	}
	store := &FunctionStore{
		basePath: basePath,
		fns:      make(map[string]*Function),
	}
	store.loadAll()
	return store
}

func (s *FunctionStore) key(projectID, id string) string {
	return projectID + "/" + id
}

func (s *FunctionStore) dirFor(projectID string) string {
	return filepath.Join(s.basePath, "functions", projectID)
}

func (s *FunctionStore) pathFor(projectID, id string) string {
	return filepath.Join(s.dirFor(projectID), id+".json")
}

// Save validates the function, bumps its version, and writes it to disk.
// Version starts at 1; subsequent Saves with the same (projectId, id) increment it.
func (s *FunctionStore) Save(fn *Function) error {
	if err := fn.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	k := s.key(fn.ProjectID, fn.ID)
	existing := s.fns[k]
	if existing != nil {
		fn.Version = existing.Version + 1
		fn.CreatedAt = existing.CreatedAt
	} else {
		fn.Version = 1
		fn.CreatedAt = time.Now()
	}
	fn.UpdatedAt = time.Now()

	if err := os.MkdirAll(s.dirFor(fn.ProjectID), 0750); err != nil {
		return fmt.Errorf("mkdir project functions: %w", err)
	}
	data, err := json.MarshalIndent(fn, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal function: %w", err)
	}
	if err := os.WriteFile(s.pathFor(fn.ProjectID, fn.ID), data, 0600); err != nil {
		return fmt.Errorf("write function file: %w", err)
	}
	s.fns[k] = fn
	return nil
}

func (s *FunctionStore) Get(projectID, id string) (*Function, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	if err := ValidateID(id); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.fns[s.key(projectID, id)], nil
}

func (s *FunctionStore) List(projectID string) ([]*Function, error) {
	if err := validateProjectID(projectID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Function, 0)
	prefix := projectID + "/"
	for k, fn := range s.fns {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, fn)
		}
	}
	return out, nil
}

func (s *FunctionStore) Delete(projectID, id string) error {
	if err := validateProjectID(projectID); err != nil {
		return err
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.pathFor(projectID, id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove function file: %w", err)
	}
	delete(s.fns, s.key(projectID, id))
	return nil
}

// loadAll walks {basePath}/functions/*/ and loads every .json file it finds.
// Tolerates missing directory (fresh install) and skips unreadable files.
func (s *FunctionStore) loadAll() {
	root := filepath.Join(s.basePath, "functions")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, projEntry := range entries {
		if !projEntry.IsDir() {
			continue
		}
		// filepath.Base on DirEntry.Name() is a no-op (Name() is already
		// the basename per Go's spec) but it's the SAST-recognised
		// sanitizer pattern. SafePathComponent layers extra defense for
		// NUL bytes + explicit non-locality.
		if _, err := security.SafePathComponent(projEntry.Name()); err != nil {
			continue
		}
		projDir := filepath.Join(root, filepath.Base(projEntry.Name()))
		s.loadProjectDir(projDir)
	}
}

// loadProjectDir reads all .json function files from a single project directory.
func (s *FunctionStore) loadProjectDir(projDir string) {
	files, err := os.ReadDir(projDir)
	if err != nil {
		return
	}
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".json" {
			continue
		}
		if _, err := security.SafePathComponent(f.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(projDir, filepath.Base(f.Name())))
		if err != nil {
			continue
		}
		var fn Function
		if err := json.Unmarshal(data, &fn); err != nil {
			continue
		}
		s.fns[s.key(fn.ProjectID, fn.ID)] = &fn
	}
}
