package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// mocoProjectPrefix is where MOCO keeps a project's backups, derived from
// its organisation and id the way its namespace is, and refused when either
// could reach past it.
func mocoProjectPrefix(orgID, projectID string) (string, error) {
	for _, part := range []string{orgID, projectID} {
		if strings.TrimSpace(part) == "" || strings.ContainsAny(part, "/\\") || strings.Contains(part, "..") {
			return "", fmt.Errorf("%w: organisation %q, project %q", ErrBackupPrefixUnsafe, orgID, projectID)
		}
	}
	return k8s.MOCOBackupPrefix(orgID+"-"+projectID, projectID), nil
}

// projectBackupPrefixes are the prefixes a project's backups are under: its
// engine's, or every engine's when the engine is unknown (a deleted
// project's record keeps none).
func projectBackupPrefixes(mode domain.DeploymentMode, engine domain.DatabaseType, orgID, projectID, dockerKeyPrefix string) ([]string, error) {
	// A single host keeps every engine's backups under the project's prefix.
	if engine == domain.MySQL && mode != domain.ModeDocker {
		prefix, err := mocoProjectPrefix(orgID, projectID)
		if err != nil {
			return nil, err
		}
		return []string{prefix}, nil
	}
	prefix, err := ProjectBackupPrefix(mode, projectID, dockerKeyPrefix)
	if err != nil {
		return nil, err
	}
	prefixes := []string{prefix}
	if engine == "" && mode != domain.ModeDocker && orgID != "" {
		moco, err := mocoProjectPrefix(orgID, projectID)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, moco)
	}
	return prefixes, nil
}

// PurgeRetained clears a deleted project's kept backups from every prefix
// its engine could have used.
func (p *BackupPurger) PurgeRetained(ctx context.Context, kept storage.RetainedBackup) (int, error) {
	prefixes, err := projectBackupPrefixes(kept.DeploymentMode, "", kept.OrgID, kept.ProjectID, p.dockerKeyPrefix)
	if err != nil {
		return 0, err
	}
	deleted, err := p.purgePrefixes(ctx, prefixes)
	if err != nil {
		return deleted, err
	}
	return deleted, p.dropRecords(ctx, kept.ProjectID)
}

// purgePrefixes deletes everything under each prefix, in pages.
func (p *BackupPurger) purgePrefixes(ctx context.Context, prefixes []string) (int, error) {
	creds, ok := p.storage.BackupStorage()
	if !ok {
		return 0, ErrBackupStorageNotConfigured
	}
	bucket, err := resolveBucket(creds)
	if err != nil {
		return 0, err
	}
	deleter, err := p.openDeleter(ctx, creds)
	if err != nil {
		return 0, fmt.Errorf("open backup store: %w", err)
	}
	total := 0
	for _, prefix := range prefixes {
		deleted, err := deleteAllUnderPrefix(ctx, deleter, bucket, prefix, p.pageSize)
		total += deleted
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
