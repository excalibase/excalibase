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

// redactedSentinel is the placeholder written into metadata.json's
// password field. Real credentials live in credentials.aes256 alongside.
// Kept as a const so SAST tools can identify it as a non-secret marker
// rather than a hardcoded password literal.
const redactedSentinel = "[REDACTED]" // #nosec G101 — placeholder, not a credential

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

// Create registers a new project, refusing an id that is already taken.
func (s *FileSystemStore) Create(inst *domain.DatabaseInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, taken := s.cache[inst.ProjectID]; taken {
		return ErrProjectExists
	}
	return s.write(inst)
}

// Update persists changes to an existing project, keeping the org it was
// created in whatever the caller put on the struct.
func (s *FileSystemStore) Update(inst *domain.DatabaseInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.cache[inst.ProjectID]
	if !ok {
		return ErrProjectNotFound
	}
	if err := CheckUpdatable(existing); err != nil {
		return err
	}
	updated := inst.Clone()
	updated.OrgID = existing.OrgID
	return s.write(updated)
}

// BeginDeletion claims the project for teardown. See InstanceStore.
func (s *FileSystemStore) BeginDeletion(projectID string, deleteBackups *bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.cache[projectID]
	if !ok {
		return false, ErrProjectNotFound
	}
	claimed := existing.Clone()
	effective, err := ApplyBeginDeletion(claimed, deleteBackups)
	if err != nil {
		return false, err
	}
	return effective, s.write(claimed)
}

// RecordDeletionFailure stores how far a teardown got. See InstanceStore.
func (s *FileSystemStore) RecordDeletionFailure(projectID string, status domain.ProvisioningStage, step, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.cache[projectID]
	if !ok {
		return ErrProjectNotFound
	}
	failed := existing.Clone()
	if err := ApplyDeletionFailure(failed, status, step, reason); err != nil {
		return err
	}
	return s.write(failed)
}

// RecordRestoreInterrupted stores why a restore stopped. See InstanceStore.
func (s *FileSystemStore) RecordRestoreInterrupted(projectID, step, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existing, ok := s.cache[projectID]
	if !ok {
		return ErrProjectNotFound
	}
	marked := existing.Clone()
	if err := ApplyRestoreInterrupted(marked, step, reason); err != nil {
		return err
	}
	return s.write(marked)
}

// write persists the instance to disk and the cache. Callers hold s.mu.
func (s *FileSystemStore) write(inst *domain.DatabaseInstance) error {
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

	// Save metadata (mask password). The real (encrypted) password lives
	// in credentials.aes256 above; this metadata.json is human-readable
	// for debugging and must NOT contain the plaintext or any reversible
	// token. The sentinel below is just a placeholder string — Snyk's
	// hardcoded-secret heuristic flags it but it's a redaction marker,
	// not a credential.
	meta := *inst
	meta.Password = redactedSentinel
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), metaJSON, 0644); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	// Keep our own copy: the caller still holds its struct and must not be
	// able to change the stored row by writing through it afterwards.
	s.cache[inst.ProjectID] = inst.Clone()
	return nil
}

func (s *FileSystemStore) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	inst, ok := s.cache[projectID]
	if !ok {
		return nil, nil
	}
	return inst.Clone(), nil
}

func (s *FileSystemStore) FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*domain.DatabaseInstance, 0)
	for _, inst := range s.cache {
		if inst.OwnerID == ownerID {
			result = append(result, inst.Clone())
		}
	}
	return result, nil
}

func (s *FileSystemStore) FindAll() ([]*domain.DatabaseInstance, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*domain.DatabaseInstance, 0, len(s.cache))
	for _, inst := range s.cache {
		result = append(result, inst.Clone())
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
		if _, err := security.SafePathComponent(entry.Name()); err != nil {
			continue
		}
		dir := filepath.Join(projectsDir, filepath.Base(entry.Name()))
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

	// Pre-Phase-0 metadata.json files have no deploymentMode key. Normalize
	// to ModeK8s so callers (BackupAdapter dispatch, in particular) never
	// receive the zero-value "".
	if inst.DeploymentMode == "" {
		inst.DeploymentMode = domain.ModeK8s
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
