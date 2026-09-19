package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// maxDeleteBatch is the S3 DeleteObjects hard limit per request.
const maxDeleteBatch = 1000

var (
	// ErrBackupPrefixUnsafe is returned when the computed object prefix is
	// empty or not scoped to the project — deleting it could touch other
	// tenants' backups, so the purge refuses before listing anything.
	ErrBackupPrefixUnsafe = errors.New("backup prefix is not scoped to the project; refusing to delete")
	// ErrNoBackupsForMode is returned for deployment modes that never write
	// backups to the platform object store.
	ErrNoBackupsForMode = errors.New("deployment mode has no platform-managed backups")
)

// ObjectDeleter is the minimal object-store surface the purge needs: a
// paginated key listing and a batch delete. AWSS3Uploader implements it;
// tests use an in-memory fake.
type ObjectDeleter interface {
	// ListKeys returns up to maxKeys keys under prefix starting at the
	// continuation token, plus the token for the next page ("" when done).
	ListKeys(ctx context.Context, bucket, prefix, continuationToken string, maxKeys int32) ([]string, string, error)
	// DeleteKeys removes the given keys (at most maxDeleteBatch) in one call.
	DeleteKeys(ctx context.Context, bucket string, keys []string) error
}

// ObjectDeleterFactory opens a deleter for the resolved backup store.
type ObjectDeleterFactory func(ctx context.Context, creds *domain.S3Credentials) (ObjectDeleter, error)

// BackupPurger deletes every backup object a project wrote to the platform
// object store. It resolves the store through the same BackupStorageSource
// the write path uses, so it can never target a different bucket than the
// one the backups landed in.
type BackupPurger struct {
	storage         BackupStorageSource
	dockerKeyPrefix string
	openDeleter     ObjectDeleterFactory
	pageSize        int32
}

// NewBackupPurger wires a purger. dockerKeyPrefix must equal the
// DockerBackupAdapter KeyPrefix ("backups/" in production).
func NewBackupPurger(storage BackupStorageSource, dockerKeyPrefix string, open ObjectDeleterFactory) *BackupPurger {
	return &BackupPurger{storage: storage, dockerKeyPrefix: dockerKeyPrefix, openDeleter: open, pageSize: maxDeleteBatch}
}

// SetPageSize overrides the list/delete page size (tests exercise pagination
// against a real store without uploading a thousand objects).
func (p *BackupPurger) SetPageSize(size int32) {
	if size > 0 && size <= maxDeleteBatch {
		p.pageSize = size
	}
}

// AWSObjectDeleterFactory builds the production ObjectDeleterFactory on top
// of AWSS3Uploader. usePathStyle mirrors the uploader wiring in main.go.
func AWSObjectDeleterFactory(usePathStyle bool) ObjectDeleterFactory {
	return func(ctx context.Context, creds *domain.S3Credentials) (ObjectDeleter, error) {
		return NewAWSS3Uploader(ctx, AWSS3UploaderConfig{
			AccessKeyID:     creds.AccessKeyID,
			SecretAccessKey: creds.SecretAccessKey,
			Region:          creds.Region,
			Endpoint:        creds.Endpoint,
			UsePathStyle:    usePathStyle,
		})
	}
}

// ProjectBackupPrefix computes the object prefix holding a project's backups:
//
//	k8s    → {projectID}/cloud/           (Barman destinationPath/serverName)
//	docker → {dockerKeyPrefix}{projectID}/ (DockerBackupAdapter key layout)
//
// A mode with no platform backups yields ErrNoBackupsForMode. The result
// is validated with validateBackupPrefix before it is returned.
func ProjectBackupPrefix(mode domain.DeploymentMode, projectID, dockerKeyPrefix string) (string, error) {
	if strings.TrimSpace(projectID) == "" || strings.ContainsAny(projectID, "/\\") || strings.Contains(projectID, "..") {
		return "", fmt.Errorf("%w: project id %q", ErrBackupPrefixUnsafe, projectID)
	}
	var prefix string
	switch mode {
	case domain.ModeK8s, "":
		prefix = k8s.BarmanObjectPrefix(projectID)
	case domain.ModeDocker:
		prefix = dockerKeyPrefix + projectID + "/"
	default:
		return "", fmt.Errorf("%w: unknown deployment mode %q", ErrNoBackupsForMode, mode)
	}
	if err := validateBackupPrefix(prefix, projectID); err != nil {
		return "", err
	}
	return prefix, nil
}

// validateBackupPrefix is the safety gate before any listing or deletion:
// the prefix must be non-empty, end with "/" (so "proj-1" never matches
// "proj-10"), and contain the project id as a whole path segment.
func validateBackupPrefix(prefix, projectID string) error {
	if prefix == "" || projectID == "" {
		return fmt.Errorf("%w: empty prefix", ErrBackupPrefixUnsafe)
	}
	if !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("%w: prefix %q is not a directory", ErrBackupPrefixUnsafe, prefix)
	}
	for _, segment := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if segment == projectID {
			return nil
		}
	}
	return fmt.Errorf("%w: prefix %q does not contain project %q", ErrBackupPrefixUnsafe, prefix, projectID)
}

// Purge deletes every object under the project's backup prefix, one page at
// a time (list ≤ pageSize, delete that batch, repeat). An empty prefix is a
// successful no-op, so calling it twice is safe. Returns the number of
// objects deleted.
func (p *BackupPurger) Purge(ctx context.Context, inst *domain.DatabaseInstance) (int, error) {
	prefix, err := ProjectBackupPrefix(inst.DeploymentMode, inst.ProjectID, p.dockerKeyPrefix)
	if err != nil {
		return 0, err
	}
	creds, ok := p.storage.BackupStorage()
	if !ok {
		return 0, ErrBackupStorageNotConfigured
	}
	bucket, err := p.resolveBucket(creds, inst.DeploymentMode)
	if err != nil {
		return 0, err
	}
	deleter, err := p.openDeleter(ctx, creds)
	if err != nil {
		return 0, fmt.Errorf("open backup store: %w", err)
	}
	return deleteAllUnderPrefix(ctx, deleter, bucket, prefix, p.pageSize)
}

// resolveBucket picks the bucket the backups were written to. K8s CRDs fall
// back to DefaultBackupBucket when none is configured, so the purge must too.
func (p *BackupPurger) resolveBucket(creds *domain.S3Credentials, mode domain.DeploymentMode) (string, error) {
	if creds.Bucket != "" {
		return creds.Bucket, nil
	}
	if mode == domain.ModeK8s || mode == "" {
		return k8s.DefaultBackupBucket, nil
	}
	return "", fmt.Errorf("%w: no bucket", ErrBackupStorageNotConfigured)
}

// deleteAllUnderPrefix runs the list → delete loop until the listing is
// exhausted. Each page is deleted before the next is fetched so a retry after
// a mid-way failure simply resumes from whatever is left.
func deleteAllUnderPrefix(ctx context.Context, deleter ObjectDeleter, bucket, prefix string, pageSize int32) (int, error) {
	deleted := 0
	token := ""
	for {
		keys, next, err := deleter.ListKeys(ctx, bucket, prefix, token, pageSize)
		if err != nil {
			return deleted, fmt.Errorf("list %s/%s: %w", bucket, prefix, err)
		}
		if len(keys) > 0 {
			if err := deleter.DeleteKeys(ctx, bucket, keys); err != nil {
				return deleted, fmt.Errorf("delete %d objects under %s/%s: %w", len(keys), bucket, prefix, err)
			}
			deleted += len(keys)
		}
		if next == "" {
			return deleted, nil
		}
		token = next
	}
}
