// Package s3keys issues and resolves the access keys S3 clients sign with
// (EXC-630, ADR 0044). A key belongs to one project, reaches all of its
// buckets or a named few, and may read, write and delete there as its
// permissions say. Its secret lives in the vault: SigV4 needs it in plain
// text to verify a signature, so it cannot be hashed.
package s3keys

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	// AccessKeyPrefix marks an access key id as one of ours at a glance.
	AccessKeyPrefix = "EXS3"
	// MaxKeysPerProject bounds the keys one project holds.
	MaxKeysPerProject = 20
	// MaxBucketsPerKey bounds a key's bucket list; past that, all buckets.
	MaxBucketsPerKey = 20
	// MaxLifetime is the longest a key may be asked to live.
	MaxLifetime    = 365 * 24 * time.Hour
	minLifetime    = 24 * time.Hour
	maxNameLength  = 64
	idRandomBytes  = 10 // 16 base32 characters
	secretBytes    = 30 // 40 base64url characters
	keyIDPrefix    = "s3k_"
	keyIDRandBytes = 16
)

var (
	ErrNotFound      = errors.New("s3 access key not found")
	ErrLimitReached  = fmt.Errorf("the project already holds %d S3 access keys; revoke one first", MaxKeysPerProject)
	ErrUnknownKey    = errors.New("unknown, expired or revoked S3 access key")
	ErrSecretMissing = errors.New("the S3 access key's secret is missing")
)

// InputError is a request the caller can fix.
type InputError struct{ Reason string }

func (e *InputError) Error() string { return e.Reason }

func inputErrorf(format string, args ...any) error {
	return &InputError{Reason: fmt.Sprintf(format, args...)}
}

// Operation is what a request does to a bucket.
type Operation int

const (
	OpRead Operation = iota
	OpWrite
	OpDelete
)

// Permissions are the operations a key may perform, in the words bucket
// access rules use.
type Permissions struct {
	Read   bool `json:"read"`
	Write  bool `json:"write"`
	Delete bool `json:"delete"`
}

// Key is one project's S3 access key, without its secret.
type Key struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	Name        string `json:"name"`
	AccessKeyID string `json:"accessKeyId"`
	// Buckets the key reaches; empty means every bucket of the project.
	Buckets []string `json:"buckets"`
	Permissions
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

// Lists reports whether the key reaches a bucket at all.
func (k *Key) Lists(bucket string) bool {
	return len(k.Buckets) == 0 || slices.Contains(k.Buckets, bucket)
}

// Allows reports whether the key may perform op on bucket.
func (k *Key) Allows(bucket string, op Operation) bool {
	if !k.Lists(bucket) {
		return false
	}
	switch op {
	case OpRead:
		return k.Read
	case OpWrite:
		return k.Write
	case OpDelete:
		return k.Delete
	}
	return false
}

func (k *Key) expired(at time.Time) bool {
	return k.ExpiresAt != nil && !at.Before(*k.ExpiresAt)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("generate S3 access key: %w", err)
	}
	return b, nil
}

// newCredentials returns a row id, an access key id and its secret.
func newCredentials() (id, accessKeyID, secret string, err error) {
	idBytes, err := randomBytes(keyIDRandBytes)
	if err != nil {
		return "", "", "", err
	}
	accessBytes, err := randomBytes(idRandomBytes)
	if err != nil {
		return "", "", "", err
	}
	secretRaw, err := randomBytes(secretBytes)
	if err != nil {
		return "", "", "", err
	}
	return keyIDPrefix + hex.EncodeToString(idBytes),
		AccessKeyPrefix + base32.StdEncoding.EncodeToString(accessBytes),
		base64.RawURLEncoding.EncodeToString(secretRaw), nil
}
