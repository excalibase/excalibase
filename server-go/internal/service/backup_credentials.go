package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/objectcreds"
)

var (
	// ErrBackupCredentialsNotConfigured refuses a Kubernetes project with
	// backups when nothing can mint its temporary credentials. The platform
	// key is never a substitute (EXC-476).
	ErrBackupCredentialsNotConfigured = errors.New("backups need temporary object-store credentials, but no provider is configured (BACKUP_CREDENTIALS_PROVIDER)")
	// ErrBackupPrefixInUse refuses to hand a project a prefix that already
	// holds objects, such as a deleted project's retained backups.
	ErrBackupPrefixInUse = errors.New("the project's backup prefix already holds objects")
	// ErrBackupStoreChosenByPlatform refuses a request that names its own
	// object store: where backups go is the platform's decision.
	ErrBackupStoreChosenByPlatform = errors.New("backup storage is chosen by the platform; remove backup.s3 from the request")
)

// BackupCredentialIssuerConfig wires the issuer.
type BackupCredentialIssuerConfig struct {
	Minter objectcreds.Minter
	// OpenStore opens the store with the platform key, to prove a prefix is
	// unused before it is handed out.
	OpenStore ObjectDeleterFactory
	// TTL is how long a project's own credentials live; renewal replaces them
	// once less than half is left.
	TTL time.Duration
	// SourceTTL is how long a restore may read its source.
	SourceTTL time.Duration
}

// BackupCredentialIssuer hands each project temporary credentials for its own
// backup prefix. The platform key it derives them from never leaves the
// platform namespace.
type BackupCredentialIssuer struct {
	cfg BackupCredentialIssuerConfig
}

// NewBackupCredentialIssuer refuses a configuration that could not issue.
func NewBackupCredentialIssuer(cfg BackupCredentialIssuerConfig) (*BackupCredentialIssuer, error) {
	switch {
	case cfg.Minter == nil:
		return nil, errors.New("backup credentials: no minter")
	case cfg.OpenStore == nil:
		return nil, errors.New("backup credentials: no store to check prefixes with")
	case cfg.TTL <= 0 || cfg.TTL > objectcreds.MaxTTL:
		return nil, fmt.Errorf("backup credentials: lifetime %s outside (0, %s]", cfg.TTL, objectcreds.MaxTTL)
	case cfg.SourceTTL <= 0 || cfg.SourceTTL > objectcreds.MaxTTL:
		return nil, fmt.Errorf("backup credentials: restore lifetime %s outside (0, %s]", cfg.SourceTTL, objectcreds.MaxTTL)
	}
	return &BackupCredentialIssuer{cfg: cfg}, nil
}

// RenewWithin is how close to expiry a project's credentials are renewed.
func (i *BackupCredentialIssuer) RenewWithin() time.Duration { return i.cfg.TTL / 2 }

// ForNewProject proves the project's prefix unused, then mints read-write
// credentials confined to it.
func (i *BackupCredentialIssuer) ForNewProject(ctx context.Context, store *domain.S3Credentials, projectID string) (*domain.S3Credentials, error) {
	if err := i.ensurePrefixUnused(ctx, store, projectID); err != nil {
		return nil, err
	}
	return i.mint(ctx, store, projectID, objectcreds.ReadWrite, i.cfg.TTL)
}

// Renew mints fresh read-write credentials for a running project's prefix.
func (i *BackupCredentialIssuer) Renew(ctx context.Context, store *domain.S3Credentials, projectID string) (*domain.S3Credentials, error) {
	return i.mint(ctx, store, projectID, objectcreds.ReadWrite, i.cfg.TTL)
}

// ForRestoreSource mints read-only credentials for the source's prefix.
func (i *BackupCredentialIssuer) ForRestoreSource(ctx context.Context, store *domain.S3Credentials, sourceProjectID string) (*domain.S3Credentials, error) {
	return i.mint(ctx, store, sourceProjectID, objectcreds.ReadOnly, i.cfg.SourceTTL)
}

func (i *BackupCredentialIssuer) mint(ctx context.Context, store *domain.S3Credentials, projectID string, access objectcreds.Access, ttl time.Duration) (*domain.S3Credentials, error) {
	if store == nil {
		return nil, ErrBackupStorageNotConfigured
	}
	creds, err := i.cfg.Minter.Mint(ctx, objectcreds.Parent{
		AccessKeyID: store.AccessKeyID, SecretAccessKey: store.SecretAccessKey,
		Endpoint: store.Endpoint, Bucket: store.Bucket, Region: store.Region,
	}, objectcreds.Scope{Prefix: k8s.BarmanObjectPrefix(projectID), Access: access, TTL: ttl})
	if err != nil {
		return nil, fmt.Errorf("mint backup credentials for %s: %w", projectID, err)
	}
	return &domain.S3Credentials{
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		SessionToken:    creds.SessionToken,
		ExpiresAt:       creds.ExpiresAt,
		IssuedBy:        k8s.BackupKeyFingerprint(store.AccessKeyID),
		Bucket:          store.Bucket,
		Region:          store.Region,
		Endpoint:        store.Endpoint,
	}, nil
}

// ensurePrefixUnused stands in for barman-cloud-check-wal-archive, which the
// clusters skip because a prefix-scoped credential cannot run it.
func (i *BackupCredentialIssuer) ensurePrefixUnused(ctx context.Context, store *domain.S3Credentials, projectID string) error {
	if store == nil {
		return ErrBackupStorageNotConfigured
	}
	objects, err := i.cfg.OpenStore(ctx, store)
	if err != nil {
		return fmt.Errorf("open backup store: %w", err)
	}
	prefix := k8s.BarmanObjectPrefix(projectID)
	keys, _, err := objects.ListKeys(ctx, store.Bucket, prefix, "", 1)
	if err != nil {
		return fmt.Errorf("check backup prefix %s: %w", prefix, err)
	}
	if len(keys) > 0 {
		return fmt.Errorf("%w: %s", ErrBackupPrefixInUse, prefix)
	}
	return nil
}
