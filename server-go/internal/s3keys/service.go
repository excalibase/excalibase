package s3keys

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// touchInterval throttles last-used writes to one a minute per key.
const touchInterval = time.Minute

// Store keeps key rows. Create counts the project's keys and inserts in one
// step; Delete returns the row it removed, nil when there was none.
type Store interface {
	Create(ctx context.Context, key *Key, maxPerProject int) error
	List(ctx context.Context, projectID string) ([]*Key, error)
	FindByAccessKeyID(ctx context.Context, accessKeyID string) (*Key, error)
	Delete(ctx context.Context, projectID, id string) (*Key, error)
	DeleteProject(ctx context.Context, projectID string) (int, error)
	TouchLastUsed(ctx context.Context, id string, at time.Time) error
}

// Secrets keeps each key's secret; Get answers ErrSecretMissing for none.
type Secrets interface {
	Put(projectID, accessKeyID, secret string) error
	Get(projectID, accessKeyID string) (string, error)
	Delete(projectID, accessKeyID string) error
}

// Standing answers whether a key's creator may still use it: an active
// account on the project's Developer rung or above.
type Standing interface {
	CanUse(ctx context.Context, userID, projectID string) (bool, error)
}

// Buckets answers whether a project has a bucket by that name.
type Buckets interface {
	BucketExists(ctx context.Context, projectID, name string) (bool, error)
}

// Service issues, lists, resolves and revokes keys.
type Service struct {
	store    Store
	secrets  Secrets
	standing Standing
	buckets  Buckets
	now      func() time.Time

	// cache holds secrets by access key id. A secret never changes for an
	// id, and every use re-reads the row, so a cached secret cannot outlive
	// its key's revocation.
	cache   sync.Map
	touched sync.Map
}

func NewService(store Store, secrets Secrets, standing Standing, buckets Buckets) *Service {
	return &Service{store: store, secrets: secrets, standing: standing, buckets: buckets, now: time.Now}
}

// CreateRequest is one key to issue.
type CreateRequest struct {
	ProjectID string
	Name      string
	Actor     string
	Buckets   []string
	Permissions
	// Lifetime is how long the key lives; 0 means until revoked.
	Lifetime time.Duration
	// NotAfter caps the expiry at the credential that made the key.
	NotAfter *time.Time
}

// Create issues a key and returns its secret, which is never shown again.
func (s *Service) Create(ctx context.Context, request CreateRequest) (string, *Key, error) {
	now := s.now().UTC()
	if err := validateName(request.Name); err != nil {
		return "", nil, err
	}
	if !request.Read && !request.Write && !request.Delete {
		return "", nil, inputErrorf("a key needs at least one of read, write and delete")
	}
	expires, err := expiry(request, now)
	if err != nil {
		return "", nil, err
	}
	buckets, err := s.validBuckets(ctx, request.ProjectID, request.Buckets)
	if err != nil {
		return "", nil, err
	}
	id, accessKeyID, secret, err := newCredentials()
	if err != nil {
		return "", nil, err
	}
	key := &Key{
		ID: id, ProjectID: request.ProjectID, Name: strings.TrimSpace(request.Name), AccessKeyID: accessKeyID,
		Buckets: buckets, Permissions: request.Permissions, CreatedBy: request.Actor, CreatedAt: now, ExpiresAt: expires,
	}
	// The secret is stored first: a row without one would be a key nobody
	// can ever use, while a secret without a row grants nothing.
	if err := s.secrets.Put(key.ProjectID, accessKeyID, secret); err != nil {
		return "", nil, fmt.Errorf("store the S3 access key secret: %w", err)
	}
	if err := s.store.Create(ctx, key, MaxKeysPerProject); err != nil {
		if cleanup := s.secrets.Delete(key.ProjectID, accessKeyID); cleanup != nil {
			log.Printf("s3 keys: remove the secret of refused key %s: %v", accessKeyID, cleanup)
		}
		return "", nil, err
	}
	return secret, key, nil
}

func validateName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || utf8.RuneCountInString(name) > maxNameLength {
		return inputErrorf("a key name is 1 to %d characters", maxNameLength)
	}
	if strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
		return inputErrorf("a key name may not contain control or formatting characters")
	}
	return nil
}

func expiry(request CreateRequest, now time.Time) (*time.Time, error) {
	if request.Lifetime != 0 && (request.Lifetime < minLifetime || request.Lifetime > MaxLifetime) {
		return nil, inputErrorf("a key lives 1 to 365 days, or until revoked")
	}
	var expires *time.Time
	if request.Lifetime > 0 {
		at := now.Add(request.Lifetime)
		expires = &at
	}
	if request.NotAfter != nil {
		limit := request.NotAfter.UTC()
		if !limit.After(now) {
			return nil, inputErrorf("the credential making this key has expired")
		}
		if expires == nil || limit.Before(*expires) {
			expires = &limit
		}
	}
	return expires, nil
}

func (s *Service) validBuckets(ctx context.Context, projectID string, names []string) ([]string, error) {
	if len(names) > MaxBucketsPerKey {
		return nil, inputErrorf("a key names at most %d buckets; leave the list empty for all of them", MaxBucketsPerKey)
	}
	unique := slices.Clone(names)
	slices.Sort(unique)
	unique = slices.Compact(unique)
	for _, name := range unique {
		exists, err := s.buckets.BucketExists(ctx, projectID, name)
		if err != nil {
			return nil, fmt.Errorf("check bucket %q: %w", name, err)
		}
		if !exists {
			return nil, inputErrorf("the project has no bucket named %q", name)
		}
	}
	if unique == nil {
		unique = []string{}
	}
	return unique, nil
}

// List returns the project's keys, revoking any whose creator may no longer
// use them on the way.
func (s *Service) List(ctx context.Context, projectID string) ([]*Key, error) {
	keys, err := s.store.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	kept := make([]*Key, 0, len(keys))
	for _, key := range keys {
		usable, err := s.standing.CanUse(ctx, key.CreatedBy, key.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("check the creator of S3 access key %s: %w", key.AccessKeyID, err)
		}
		if !usable {
			s.revokeForStanding(ctx, key)
			continue
		}
		kept = append(kept, key)
	}
	return kept, nil
}

// Revoke removes a key; the row going is what ends its access.
func (s *Service) Revoke(ctx context.Context, projectID, id string) (*Key, error) {
	key, err := s.store.Delete(ctx, projectID, id)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, ErrNotFound
	}
	s.forget(key)
	return key, nil
}

func (s *Service) forget(key *Key) {
	s.cache.Delete(key.AccessKeyID)
	s.touched.Delete(key.ID)
	if err := s.secrets.Delete(key.ProjectID, key.AccessKeyID); err != nil && !errors.Is(err, ErrSecretMissing) {
		log.Printf("s3 keys: key %s is revoked but its secret stays in the vault until the project goes: %v", key.AccessKeyID, err)
	}
}

func (s *Service) revokeForStanding(ctx context.Context, key *Key) {
	if _, err := s.Revoke(ctx, key.ProjectID, key.ID); err != nil && !errors.Is(err, ErrNotFound) {
		log.Printf("s3 keys: revoke key %s of a creator who lost the role: %v", key.AccessKeyID, err)
		return
	}
	log.Printf("s3 keys: revoked key %s: its creator %s may no longer use project %s", key.AccessKeyID, key.CreatedBy, key.ProjectID)
}

// Resolve returns a usable key and its secret. ErrUnknownKey means the key
// does not grant access; any other error means it could not be checked.
func (s *Service) Resolve(ctx context.Context, accessKeyID string) (*Key, string, error) {
	key, err := s.store.FindByAccessKeyID(ctx, accessKeyID)
	if err != nil {
		return nil, "", fmt.Errorf("read S3 access key: %w", err)
	}
	now := s.now().UTC()
	if key == nil || key.expired(now) {
		return nil, "", ErrUnknownKey
	}
	usable, err := s.standing.CanUse(ctx, key.CreatedBy, key.ProjectID)
	if err != nil {
		return nil, "", fmt.Errorf("check the creator of S3 access key: %w", err)
	}
	if !usable {
		s.revokeForStanding(ctx, key)
		return nil, "", ErrUnknownKey
	}
	secret, err := s.secret(key)
	if err != nil {
		return nil, "", err
	}
	s.touch(ctx, key, now)
	return key, secret, nil
}

func (s *Service) secret(key *Key) (string, error) {
	if cached, ok := s.cache.Load(key.AccessKeyID); ok {
		return cached.(string), nil
	}
	secret, err := s.secrets.Get(key.ProjectID, key.AccessKeyID)
	if errors.Is(err, ErrSecretMissing) {
		log.Printf("s3 keys: key %s has a row but no secret; refusing it", key.AccessKeyID)
		return "", ErrUnknownKey
	}
	if err != nil {
		return "", fmt.Errorf("read S3 access key secret: %w", err)
	}
	s.cache.Store(key.AccessKeyID, secret)
	return secret, nil
}

func (s *Service) touch(ctx context.Context, key *Key, now time.Time) {
	if last, ok := s.touched.Load(key.ID); ok && now.Sub(last.(time.Time)) < touchInterval {
		return
	}
	s.touched.Store(key.ID, now)
	if err := s.store.TouchLastUsed(ctx, key.ID, now); err != nil {
		log.Printf("s3 keys: record use of %s: %v", key.AccessKeyID, err)
	}
}

// PurgeProject removes every key of a project being deleted. The secrets go
// with the project's vault prefix.
func (s *Service) PurgeProject(ctx context.Context, projectID string) (int, error) {
	return s.store.DeleteProject(ctx, projectID)
}
