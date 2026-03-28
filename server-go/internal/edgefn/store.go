package edgefn

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Script struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	HookType  string    `json:"hookType"` // pre-provision, post-provision, custom, etc.
	Active    bool      `json:"active"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ScriptStore struct {
	basePath string
	mu       sync.RWMutex
	scripts  map[string]*Script
}

func NewScriptStore(basePath string) *ScriptStore {
	dir := filepath.Join(basePath, "scripts")
	os.MkdirAll(dir, 0755)

	store := &ScriptStore{
		basePath: basePath,
		scripts:  make(map[string]*Script),
	}
	store.loadAll()
	return store
}

func (s *ScriptStore) Save(script *Script) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing := s.scripts[script.ID]
	if existing != nil {
		script.Version = existing.Version + 1
		script.CreatedAt = existing.CreatedAt
	} else {
		script.Version = 1
		script.CreatedAt = time.Now()
	}
	script.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(script, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal script: %w", err)
	}

	path := filepath.Join(s.basePath, "scripts", script.ID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write script: %w", err)
	}

	s.scripts[script.ID] = script
	return nil
}

func (s *ScriptStore) Get(id string) (*Script, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scripts[id], nil
}

func (s *ScriptStore) List(hookType string) ([]*Script, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Script, 0)
	for _, sc := range s.scripts {
		if hookType == "" || sc.HookType == hookType {
			result = append(result, sc)
		}
	}
	return result, nil
}

func (s *ScriptStore) ListByHookType(hookType string) ([]*Script, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Script, 0)
	for _, sc := range s.scripts {
		if sc.HookType == hookType && sc.Active {
			result = append(result, sc)
		}
	}
	return result, nil
}

func (s *ScriptStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.basePath, "scripts", id+".json")
	os.Remove(path)
	delete(s.scripts, id)
	return nil
}

func (s *ScriptStore) loadAll() {
	dir := filepath.Join(s.basePath, "scripts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var sc Script
		if json.Unmarshal(data, &sc) == nil {
			s.scripts[sc.ID] = &sc
		}
	}
}
