package vault

import (
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// BoltStore implements VaultStore using bbolt (embedded KV).
type BoltStore struct {
	db *bolt.DB
}

func NewBoltStore(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("open bbolt: %w", err)
	}

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

	return &BoltStore{db: db}, nil
}

func (s *BoltStore) GetBarrier() ([]byte, []byte, error) {
	var barrier, meta []byte
	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketBarrier)
		if raw := b.Get(keyBarrier); raw != nil {
			barrier = make([]byte, len(raw))
			copy(barrier, raw)
		}
		if raw := b.Get(keyMeta); raw != nil {
			meta = make([]byte, len(raw))
			copy(meta, raw)
		}
		return nil
	})
	return barrier, meta, nil
}

func (s *BoltStore) PutBarrier(encryptedBarrier []byte, meta []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketBarrier)
		if err := b.Put(keyBarrier, encryptedBarrier); err != nil {
			return err
		}
		return b.Put(keyMeta, meta)
	})
}

func (s *BoltStore) GetSecret(path string) ([]byte, error) {
	var data []byte
	s.db.View(func(tx *bolt.Tx) error {
		if raw := tx.Bucket(bucketSecrets).Get([]byte(path)); raw != nil {
			data = make([]byte, len(raw))
			copy(data, raw)
		}
		return nil
	})
	return data, nil
}

func (s *BoltStore) PutSecret(path string, data []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSecrets).Put([]byte(path), data)
	})
}

func (s *BoltStore) DeleteSecret(path string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketSecrets).Delete([]byte(path))
	})
}

func (s *BoltStore) DeletePrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, fmt.Errorf("DeletePrefix: empty prefix not allowed")
	}
	deleted := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSecrets)
		// Collect first; bolt's cursor is unsafe to delete through.
		var keys [][]byte
		if err := b.ForEach(func(k, _ []byte) error {
			if strings.HasPrefix(string(k), prefix) {
				cp := make([]byte, len(k))
				copy(cp, k)
				keys = append(keys, cp)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
			deleted++
		}
		return nil
	})
	return deleted, err
}

func (s *BoltStore) ListSecrets(prefix string) ([]string, error) {
	var paths []string
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketSecrets)
		return b.ForEach(func(k, _ []byte) error {
			key := string(k)
			if prefix == "" || len(key) >= len(prefix) && key[:len(prefix)] == prefix {
				paths = append(paths, key)
			}
			return nil
		})
	})
	return paths, err
}

func (s *BoltStore) Close() error {
	return s.db.Close()
}
