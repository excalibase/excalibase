package vault

// VaultStore abstracts the storage backend for the vault.
// Both bbolt (local dev) and Postgres (production) implement this.
type VaultStore interface {
	// GetBarrier reads the encrypted barrier key and metadata.
	// Returns nil, nil, nil if not initialized.
	GetBarrier() (encryptedBarrier []byte, meta []byte, err error)

	// PutBarrier stores the encrypted barrier key and metadata.
	PutBarrier(encryptedBarrier []byte, meta []byte) error

	// GetSecret reads a secret entry by path.
	// Returns nil, nil if not found.
	GetSecret(path string) ([]byte, error)

	// PutSecret stores a secret entry (already encrypted) at path.
	PutSecret(path string, data []byte) error

	// DeleteSecret removes a secret by path.
	DeleteSecret(path string) error

	// DeletePrefix removes every secret whose path starts with the given
	// prefix and returns the number deleted. Empty prefix is rejected.
	// Implementations should perform this in a single transaction so
	// callers see all-or-nothing semantics.
	DeletePrefix(prefix string) (int, error)

	// ListSecrets returns all secret paths matching the given prefix.
	// Pass empty string to list all secrets.
	ListSecrets(prefix string) ([]string, error)

	// Close releases any resources.
	Close() error
}
