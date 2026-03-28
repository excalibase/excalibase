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

// FileSystemStore implements InstanceStore using JSON files + AES-256-GCM encryption.
type FileSystemStore struct {
	basePath  string
	encryptor *security.Encryptor
	mu        sync.RWMutex
	cache     map[string]*domain.DatabaseInstance
}

func NewFileSystemStore(basePath string) (*FileSystemStore, error) {
	projectsDir := filepath.Join(basePath, "projects")
	if err := os.MkdirAll(projectsDir, 0755); err != nil {
		return nil, fmt.Errorf("create projects dir: %w", err)
	}

	enc, err := security.NewEncryptor(basePath)
	if err != nil {
		return nil, fmt.Errorf("init encryptor: %w", err)
	}

	store := &FileSystemStore{
		basePath:  basePath,
		encryptor: enc,
		cache:     make(map[string]*domain.DatabaseInstance),
	}

	if err := store.loadAll(); err != nil {
		return nil, fmt.Errorf("load existing data: %w", err)
	}

	return store, nil
}

func (s *FileSystemStore) Save(inst *domain.DatabaseInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.projectDir(inst.ProjectID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create project dir: %w", err)
	}

	// Encrypt credentials
	creds := domain.CredentialsData{
		Host:         inst.Host,
		ReadOnlyHost: inst.ReadOnlyHost,
		Port:         derefInt(inst.Port),
		DatabaseName: inst.DatabaseName,
		Username:     inst.Username,
		Password:     inst.Password,
		SSLMode:      inst.SSLMode,
	}
	credsJSON, _ := json.Marshal(creds)
	encrypted, err := s.encryptor.Encrypt(credsJSON)
	if err != nil {
		return fmt.Errorf("encrypt credentials: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.aes256"), []byte(encrypted), 0600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	// Save metadata (mask password)
	meta := *inst
	meta.Password = "***ENCRYPTED***"
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), metaJSON, 0644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	s.cache[inst.ProjectID] = inst
	return nil
}

func (s *FileSystemStore) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	inst, ok := s.cache[projectID]
	if !ok {
		return nil, nil
	}
	return inst, nil
}

func (s *FileSystemStore) FindAll() ([]*domain.DatabaseInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*domain.DatabaseInstance, 0, len(s.cache))
	for _, inst := range s.cache {
		result = append(result, inst)
	}
	return result, nil
}

func (s *FileSystemStore) Delete(projectID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.projectDir(projectID)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove project dir: %w", err)
	}

	delete(s.cache, projectID)
	return nil
}

func (s *FileSystemStore) projectDir(projectID string) string {
	return filepath.Join(s.basePath, "projects", projectID)
}

func (s *FileSystemStore) loadAll() error {
	projectsDir := filepath.Join(s.basePath, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(projectsDir, entry.Name())
		inst, err := s.loadInstance(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: failed to load %s: %v\n", entry.Name(), err)
			continue
		}
		s.cache[inst.ProjectID] = inst
	}
	return nil
}

func (s *FileSystemStore) loadInstance(dir string) (*domain.DatabaseInstance, error) {
	metaPath := filepath.Join(dir, "metadata.json")
	metaJSON, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}

	var inst domain.DatabaseInstance
	if err := json.Unmarshal(metaJSON, &inst); err != nil {
		return nil, fmt.Errorf("parse metadata: %w", err)
	}

	// Decrypt credentials
	credsPath := filepath.Join(dir, "credentials.aes256")
	encrypted, err := os.ReadFile(credsPath)
	if err == nil {
		decrypted, err := s.encryptor.Decrypt(string(encrypted))
		if err == nil {
			var creds domain.CredentialsData
			if json.Unmarshal(decrypted, &creds) == nil {
				inst.Host = creds.Host
				inst.ReadOnlyHost = creds.ReadOnlyHost
				inst.Port = &creds.Port
				inst.DatabaseName = creds.DatabaseName
				inst.Username = creds.Username
				inst.Password = creds.Password
				inst.SSLMode = creds.SSLMode
			}
		}
	}

	return &inst, nil
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
