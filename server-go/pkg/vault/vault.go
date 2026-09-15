package vault

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/hashicorp/vault/shamir"
)

var (
	ErrSealed         = errors.New("vault is sealed")
	ErrNotInitialized = errors.New("vault is not initialized")
	ErrAlreadyInit    = errors.New("vault is already initialized")
	ErrNotFound       = errors.New("secret not found")
)

type Vault struct {
	store      VaultStore
	barrierKey []byte // decrypted barrier key, nil when sealed
	mu         sync.RWMutex

	// unseal progress
	unsealShares [][]byte
	threshold    int
}

type InitResult struct {
	Shares    []string // hex-encoded key shares
	Threshold int
}

type UnsealProgress struct {
	Done      bool
	Progress  int
	Threshold int
}

// Status mirrors HashiCorp Vault's /sys/seal-status shape so the studio UI
// can drive the same init → unseal → ready routing flow. Threshold/Shares
// are zero before init; Progress is non-zero only mid-unseal.
type Status struct {
	Initialized bool
	Sealed      bool
	Threshold   int
	Shares      int
	Progress    int
	Type        string
}

type barrierMeta struct {
	EncryptedBarrier []byte `json:"encrypted_barrier"`
	Threshold        int    `json:"threshold"`
	Shares           int    `json:"shares"`
}

// NewWithStore creates a vault on the given VaultStore backend.
func NewWithStore(store VaultStore) (*Vault, error) {
	return newVault(store)
}

func newVault(store VaultStore) (*Vault, error) {
	v := &Vault{store: store}

	// Auto-unseal from env if initialized
	if v.Initialized() {
		if key := os.Getenv("VAULT_UNSEAL_KEY"); key != "" {
			v.autoUnseal(key)
		}
	}

	return v, nil
}

func (v *Vault) Close() error {
	return v.store.Close()
}

func (v *Vault) Initialized() bool {
	barrier, _, err := v.store.GetBarrier()
	if err != nil {
		return false
	}
	return barrier != nil
}

func (v *Vault) Sealed() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.barrierKey == nil
}

// Status returns a snapshot suitable for the studio's setup wizard. Reads
// barrier meta only when initialized so the pre-init path stays cheap.
func (v *Vault) Status() Status {
	s := Status{
		Initialized: v.Initialized(),
		Sealed:      v.Sealed(),
		Type:        "shamir",
	}
	if !s.Initialized {
		return s
	}
	if meta, err := v.getMeta(); err == nil {
		s.Threshold = meta.Threshold
		s.Shares = meta.Shares
	}
	v.mu.RLock()
	s.Progress = len(v.unsealShares)
	v.mu.RUnlock()
	return s
}

func (v *Vault) Init(shares, threshold int) (*InitResult, error) {
	if v.Initialized() {
		return nil, ErrAlreadyInit
	}
	if shares < 1 || threshold < 1 || threshold > shares {
		return nil, fmt.Errorf("invalid shares=%d threshold=%d", shares, threshold)
	}

	barrierKey, mek, encryptedBarrier, err := generateBarrierKeys()
	if err != nil {
		return nil, err
	}

	if err := v.storeBarrierMeta(encryptedBarrier, shares, threshold); err != nil {
		return nil, err
	}

	hexShares, err := splitMEKToHex(mek, shares, threshold)
	if err != nil {
		return nil, err
	}

	// Unseal immediately after init
	v.mu.Lock()
	v.barrierKey = barrierKey
	v.threshold = threshold
	v.mu.Unlock()

	if err := v.storePKIKeys(); err != nil {
		return nil, err
	}

	return &InitResult{
		Shares:    hexShares,
		Threshold: threshold,
	}, nil
}

// generateBarrierKeys creates the barrier key, MEK, and encrypted barrier.
func generateBarrierKeys() (barrierKey, mek, encryptedBarrier []byte, err error) {
	barrierKey, err = randomBytes(32)
	if err != nil {
		return nil, nil, nil, err
	}
	mek, err = randomBytes(32)
	if err != nil {
		return nil, nil, nil, err
	}
	encryptedBarrier, err = encrypt(mek, barrierKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("encrypt barrier: %w", err)
	}
	return barrierKey, mek, encryptedBarrier, nil
}

// storeBarrierMeta persists the encrypted barrier and its metadata.
func (v *Vault) storeBarrierMeta(encryptedBarrier []byte, shares, threshold int) error {
	meta := barrierMeta{
		EncryptedBarrier: encryptedBarrier,
		Threshold:        threshold,
		Shares:           shares,
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal barrier meta: %w", err)
	}
	if err := v.store.PutBarrier(encryptedBarrier, metaBytes); err != nil {
		return fmt.Errorf("store barrier: %w", err)
	}
	return nil
}

// splitMEKToHex splits the MEK into Shamir shares and hex-encodes them.
func splitMEKToHex(mek []byte, shares, threshold int) ([]string, error) {
	var shareBytes [][]byte
	if shares == 1 && threshold == 1 {
		shareBytes = [][]byte{mek}
	} else {
		var err error
		shareBytes, err = shamir.Split(mek, shares, threshold)
		if err != nil {
			return nil, fmt.Errorf("shamir split: %w", err)
		}
	}
	hexShares := make([]string, len(shareBytes))
	for i, s := range shareBytes {
		hexShares[i] = hex.EncodeToString(s)
	}
	return hexShares, nil
}

// storePKIKeys generates and stores the EC-P256 signing keypair in the vault.
func (v *Vault) storePKIKeys() error {
	privPEM, pubPEM, err := generatePKI()
	if err != nil {
		return fmt.Errorf("generate PKI: %w", err)
	}
	if err := v.Put("pki/signing/private", map[string]string{"key": privPEM, "algorithm": "EC-P256"}); err != nil {
		return fmt.Errorf("store pki private key: %w", err)
	}
	if err := v.Put("pki/signing/public", map[string]string{"key": pubPEM, "algorithm": "EC-P256"}); err != nil {
		return fmt.Errorf("store pki public key: %w", err)
	}
	return nil
}

func (v *Vault) Seal() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.barrierKey = nil
	v.unsealShares = nil
}

func (v *Vault) Unseal(shareHex string) (*UnsealProgress, error) {
	if !v.Initialized() {
		return nil, ErrNotInitialized
	}
	if !v.Sealed() {
		return &UnsealProgress{Done: true, Progress: 0, Threshold: 0}, nil
	}

	shareBytes, err := hex.DecodeString(shareHex)
	if err != nil {
		return nil, fmt.Errorf("invalid share hex: %w", err)
	}

	meta, err := v.getMeta()
	if err != nil {
		return nil, err
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	v.threshold = meta.Threshold
	v.unsealShares = append(v.unsealShares, shareBytes)

	if len(v.unsealShares) < meta.Threshold {
		return &UnsealProgress{
			Done:      false,
			Progress:  len(v.unsealShares),
			Threshold: meta.Threshold,
		}, nil
	}

	if err := v.applyUnsealShares(*meta); err != nil {
		return nil, err
	}

	return &UnsealProgress{
		Done:      true,
		Progress:  meta.Threshold,
		Threshold: meta.Threshold,
	}, nil
}

// applyUnsealShares reconstructs the MEK from accumulated shares and decrypts
// the barrier key. Must be called with v.mu held. Clears unsealShares on any error.
func (v *Vault) applyUnsealShares(meta barrierMeta) error {
	mek, err := reconstructMEK(v.unsealShares, meta)
	if err != nil {
		v.unsealShares = nil
		return err
	}

	encryptedBarrier, _, err := v.store.GetBarrier()
	if err != nil {
		v.unsealShares = nil
		return fmt.Errorf("read barrier: %w", err)
	}

	barrierKey, err := decrypt(mek, encryptedBarrier)
	if err != nil {
		v.unsealShares = nil
		return fmt.Errorf("decrypt barrier: %w (wrong shares?)", err)
	}

	v.barrierKey = barrierKey
	v.unsealShares = nil
	return nil
}

// reconstructMEK combines shares to recover the master encryption key.
// Single-share (1-of-1) setups bypass Shamir split/combine.
func reconstructMEK(shares [][]byte, meta barrierMeta) ([]byte, error) {
	if meta.Shares == 1 && meta.Threshold == 1 {
		return shares[0], nil
	}
	mek, err := shamir.Combine(shares)
	if err != nil {
		return nil, fmt.Errorf("shamir combine failed: %w", err)
	}
	return mek, nil
}

func (v *Vault) ResetUnseal() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.unsealShares = nil
}

func (v *Vault) Rekey(shares, threshold int) (*InitResult, error) {
	v.mu.RLock()
	if v.barrierKey == nil {
		v.mu.RUnlock()
		return nil, ErrSealed
	}
	currentBarrier := make([]byte, len(v.barrierKey))
	copy(currentBarrier, v.barrierKey)
	v.mu.RUnlock()

	// Generate new MEK
	newMEK, err := randomBytes(32)
	if err != nil {
		return nil, err
	}

	// Re-encrypt barrier with new MEK
	encryptedBarrier, err := encrypt(newMEK, currentBarrier)
	if err != nil {
		return nil, fmt.Errorf("encrypt barrier: %w", err)
	}

	meta := barrierMeta{
		EncryptedBarrier: encryptedBarrier,
		Threshold:        threshold,
		Shares:           shares,
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal barrier meta: %w", err)
	}

	if err := v.store.PutBarrier(encryptedBarrier, metaBytes); err != nil {
		return nil, fmt.Errorf("store barrier: %w", err)
	}

	// Split new MEK
	var shareBytes [][]byte
	if shares == 1 && threshold == 1 {
		shareBytes = [][]byte{newMEK}
	} else {
		shareBytes, err = shamir.Split(newMEK, shares, threshold)
		if err != nil {
			return nil, fmt.Errorf("shamir split: %w", err)
		}
	}

	hexShares := make([]string, len(shareBytes))
	for i, s := range shareBytes {
		hexShares[i] = hex.EncodeToString(s)
	}

	return &InitResult{
		Shares:    hexShares,
		Threshold: threshold,
	}, nil
}

// --- Secret operations ---

func (v *Vault) Put(path string, data map[string]string) error {
	v.mu.RLock()
	if v.barrierKey == nil {
		v.mu.RUnlock()
		return ErrSealed
	}
	key := make([]byte, len(v.barrierKey))
	copy(key, v.barrierKey)
	v.mu.RUnlock()

	// Generate per-secret DEK
	dek, err := randomBytes(32)
	if err != nil {
		return err
	}

	// Encrypt data with DEK
	plaintext, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal secret data: %w", err)
	}
	encryptedData, err := encrypt(dek, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt data: %w", err)
	}

	// Encrypt DEK with barrier key
	encryptedDEK, err := encrypt(key, dek)
	if err != nil {
		return fmt.Errorf("encrypt dek: %w", err)
	}

	// Store both
	entry := secretEntry{
		EncryptedData: encryptedData,
		EncryptedDEK:  encryptedDEK,
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal secret entry: %w", err)
	}

	return v.store.PutSecret(path, entryBytes)
}

func (v *Vault) Get(path string) (map[string]string, error) {
	v.mu.RLock()
	if v.barrierKey == nil {
		v.mu.RUnlock()
		return nil, ErrSealed
	}
	key := make([]byte, len(v.barrierKey))
	copy(key, v.barrierKey)
	v.mu.RUnlock()

	entryBytes, err := v.store.GetSecret(path)
	if err != nil {
		return nil, err
	}
	if entryBytes == nil {
		return nil, ErrNotFound
	}

	var entry secretEntry
	if err := json.Unmarshal(entryBytes, &entry); err != nil {
		return nil, fmt.Errorf("unmarshal entry: %w", err)
	}

	// Decrypt DEK with barrier key
	dek, err := decrypt(key, entry.EncryptedDEK)
	if err != nil {
		return nil, fmt.Errorf("decrypt dek: %w", err)
	}

	// Decrypt data with DEK
	plaintext, err := decrypt(dek, entry.EncryptedData)
	if err != nil {
		return nil, fmt.Errorf("decrypt data: %w", err)
	}

	var data map[string]string
	if err := json.Unmarshal(plaintext, &data); err != nil {
		return nil, fmt.Errorf("unmarshal data: %w", err)
	}
	return data, nil
}

func (v *Vault) Delete(path string) error {
	v.mu.RLock()
	if v.barrierKey == nil {
		v.mu.RUnlock()
		return ErrSealed
	}
	v.mu.RUnlock()

	return v.store.DeleteSecret(path)
}

// DeletePrefix removes every secret whose path starts with prefix in a
// single underlying-store transaction. Returns the number deleted.
// Empty prefix is rejected to avoid accidentally wiping the vault.
func (v *Vault) DeletePrefix(prefix string) (int, error) {
	v.mu.RLock()
	if v.barrierKey == nil {
		v.mu.RUnlock()
		return 0, ErrSealed
	}
	v.mu.RUnlock()

	if prefix == "" {
		return 0, fmt.Errorf("DeletePrefix: empty prefix not allowed")
	}
	return v.store.DeletePrefix(prefix)
}

// List returns all secret paths matching the given prefix.
// Pass empty string to list all secrets.
func (v *Vault) List(prefix string) ([]string, error) {
	v.mu.RLock()
	if v.barrierKey == nil {
		v.mu.RUnlock()
		return nil, ErrSealed
	}
	v.mu.RUnlock()

	return v.store.ListSecrets(prefix)
}

// --- helpers ---

type secretEntry struct {
	EncryptedData []byte `json:"d"`
	EncryptedDEK  []byte `json:"k"`
}

func (v *Vault) getMeta() (*barrierMeta, error) {
	_, metaBytes, err := v.store.GetBarrier()
	if err != nil {
		return nil, err
	}
	if metaBytes == nil {
		return nil, fmt.Errorf("barrier meta not found")
	}
	var meta barrierMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("unmarshal meta: %w", err)
	}
	return &meta, nil
}

func (v *Vault) autoUnseal(keyHex string) {
	progress, err := v.Unseal(keyHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARN: auto-unseal failed: %v\n", err)
		return
	}
	if progress.Done {
		log.Printf("Vault auto-unsealed from VAULT_UNSEAL_KEY")
	}
}
