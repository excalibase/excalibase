package vault

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/hashicorp/vault/shamir"
	bolt "go.etcd.io/bbolt"
)

var (
	ErrSealed         = errors.New("vault is sealed")
	ErrNotInitialized = errors.New("vault is not initialized")
	ErrAlreadyInit    = errors.New("vault is already initialized")
	ErrNotFound       = errors.New("secret not found")

	bucketBarrier = []byte("barrier")
	bucketSecrets = []byte("secrets")
	keyBarrier    = []byte("barrier_key")
	keyMeta       = []byte("meta")
)

type Vault struct {
	db         *bolt.DB
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

type barrierMeta struct {
	EncryptedBarrier []byte `json:"encrypted_barrier"`
	Threshold        int    `json:"threshold"`
	Shares           int    `json:"shares"`
}

func New(path string) (*Vault, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt: %w", err)
	}

	// Ensure buckets exist
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bucketBarrier); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bucketSecrets)
		return err
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("create buckets: %w", err)
	}

	v := &Vault{db: db}

	// Auto-unseal from env if initialized
	if v.Initialized() {
		if key := os.Getenv("VAULT_UNSEAL_KEY"); key != "" {
			v.autoUnseal(key)
		}
	}

	return v, nil
}

func (v *Vault) Close() error {
	return v.db.Close()
}

func (v *Vault) Initialized() bool {
	var exists bool
	v.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketBarrier)
		exists = b.Get(keyBarrier) != nil
		return nil
	})
	return exists
}

func (v *Vault) Sealed() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.barrierKey == nil
}

func (v *Vault) Init(shares, threshold int) (*InitResult, error) {
	if v.Initialized() {
		return nil, ErrAlreadyInit
	}
	if shares < 1 || threshold < 1 || threshold > shares {
		return nil, fmt.Errorf("invalid shares=%d threshold=%d", shares, threshold)
	}

	// Generate barrier key (256-bit)
	barrierKey, err := randomBytes(32)
	if err != nil {
		return nil, err
	}

	// Generate MEK (256-bit)
	mek, err := randomBytes(32)
	if err != nil {
		return nil, err
	}

	// Encrypt barrier key with MEK
	encryptedBarrier, err := encrypt(mek, barrierKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt barrier: %w", err)
	}

	// Store encrypted barrier + metadata
	meta := barrierMeta{
		EncryptedBarrier: encryptedBarrier,
		Threshold:        threshold,
		Shares:           shares,
	}
	metaBytes, _ := json.Marshal(meta)

	err = v.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketBarrier)
		if err := b.Put(keyBarrier, encryptedBarrier); err != nil {
			return err
		}
		return b.Put(keyMeta, metaBytes)
	})
	if err != nil {
		return nil, fmt.Errorf("store barrier: %w", err)
	}

	// Split MEK into shares
	var shareBytes [][]byte
	if shares == 1 && threshold == 1 {
		shareBytes = [][]byte{mek}
	} else {
		shareBytes, err = shamir.Split(mek, shares, threshold)
		if err != nil {
			return nil, fmt.Errorf("shamir split: %w", err)
		}
	}

	// Hex-encode shares for display
	hexShares := make([]string, len(shareBytes))
	for i, s := range shareBytes {
		hexShares[i] = hex.EncodeToString(s)
	}

	// Unseal immediately after init
	v.mu.Lock()
	v.barrierKey = barrierKey
	v.threshold = threshold
	v.mu.Unlock()

	// Generate PKI signing keypair
	privPEM, pubPEM, err := generatePKI()
	if err != nil {
		return nil, fmt.Errorf("generate PKI: %w", err)
	}
	v.Put("pki/signing/private", map[string]string{"key": privPEM, "algorithm": "EC-P256"})
	v.Put("pki/signing/public", map[string]string{"key": pubPEM, "algorithm": "EC-P256"})

	return &InitResult{
		Shares:    hexShares,
		Threshold: threshold,
	}, nil
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

	// Get threshold from meta
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

	// Reconstruct MEK
	var mek []byte
	if meta.Shares == 1 && meta.Threshold == 1 {
		mek = v.unsealShares[0]
	} else {
		mek, err = shamir.Combine(v.unsealShares)
		if err != nil {
			v.unsealShares = nil // reset on failure
			return nil, fmt.Errorf("shamir combine failed: %w", err)
		}
	}

	// Decrypt barrier key
	var encryptedBarrier []byte
	v.db.View(func(tx *bolt.Tx) error {
		encryptedBarrier = tx.Bucket(bucketBarrier).Get(keyBarrier)
		return nil
	})

	barrierKey, err := decrypt(mek, encryptedBarrier)
	if err != nil {
		v.unsealShares = nil
		return nil, fmt.Errorf("decrypt barrier: %w (wrong shares?)", err)
	}

	v.barrierKey = barrierKey
	v.unsealShares = nil

	return &UnsealProgress{
		Done:      true,
		Progress:  meta.Threshold,
		Threshold: meta.Threshold,
	}, nil
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
	metaBytes, _ := json.Marshal(meta)

	err = v.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketBarrier)
		if err := b.Put(keyBarrier, encryptedBarrier); err != nil {
			return err
		}
		return b.Put(keyMeta, metaBytes)
	})
	if err != nil {
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
	plaintext, _ := json.Marshal(data)
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
	entryBytes, _ := json.Marshal(entry)

	return v.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSecrets).Put([]byte(path), entryBytes)
	})
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

	var entryBytes []byte
	v.db.View(func(tx *bolt.Tx) error {
		entryBytes = tx.Bucket(bucketSecrets).Get([]byte(path))
		return nil
	})
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

	return v.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSecrets).Delete([]byte(path))
	})
}

// --- helpers ---

type secretEntry struct {
	EncryptedData []byte `json:"d"`
	EncryptedDEK  []byte `json:"k"`
}

func (v *Vault) getMeta() (*barrierMeta, error) {
	var metaBytes []byte
	v.db.View(func(tx *bolt.Tx) error {
		metaBytes = tx.Bucket(bucketBarrier).Get(keyMeta)
		return nil
	})
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
		fmt.Println("Vault auto-unsealed from VAULT_UNSEAL_KEY")
	}
}
