package edgefn

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// MaxCodeSize limits the code payload to 512 KB.
const MaxCodeSize = 512 * 1024

// validID allows only alphanumeric, hyphens, underscores, max 64 chars.
var validID = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

// ValidateID checks that a script ID is safe for filesystem and URL use.
func ValidateID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid function ID: must be 1-64 alphanumeric/hyphen/underscore characters")
	}
	return nil
}

var validHookTypes = map[string]bool{
	"custom": true, "pre-provision": true, "post-provision": true, "webhook": true,
}

type Script struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	HookType  string    `json:"hookType"`
	Active    bool      `json:"active"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Script) Validate() error {
	if err := ValidateID(s.ID); err != nil {
		return err
	}
	if s.Name == "" || len(s.Name) > 200 {
		return fmt.Errorf("name must be 1-200 characters")
	}
	if len(s.Code) == 0 {
		return fmt.Errorf("code is required")
	}
	if len(s.Code) > MaxCodeSize {
		return fmt.Errorf("code exceeds maximum size (%d KB)", MaxCodeSize/1024)
	}
	if s.HookType != "" && !validHookTypes[s.HookType] {
		return fmt.Errorf("invalid hookType: %s", s.HookType)
	}
	return nil
}

type ScriptStore struct {
	basePath string
	mu       sync.RWMutex
	scripts  map[string]*Script
}

func NewScriptStore(basePath string) *ScriptStore {
	dir := filepath.Join(basePath, "scripts")
	if err := os.MkdirAll(dir, 0750); err != nil {
		log.Printf("WARN: failed to create scripts dir: %v", err)
	}

	store := &ScriptStore{
		basePath: basePath,
		scripts:  make(map[string]*Script),
	}
	store.loadAll()
	return store
}

func (s *ScriptStore) Save(script *Script) error {
	if err := script.Validate(); err != nil {
		return err
	}

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
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write script: %w", err)
	}

	s.scripts[script.ID] = script
	return nil
}

func (s *ScriptStore) Get(id string) (*Script, error) {
	if err := ValidateID(id); err != nil {
		return nil, err
	}
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
	if err := ValidateID(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.basePath, "scripts", id+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove script file: %w", err)
	}
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
