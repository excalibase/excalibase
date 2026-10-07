package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// GetCorsOrigins returns the project's browser-origin allowlist; a project
// without a row reads as empty, which the data plane treats as "no CORS".
func (s *Store) GetCorsOrigins(ctx context.Context, projectID string) ([]string, error) {
	origins := []string{}
	err := s.db.QueryRowContext(ctx,
		`SELECT allowed_origins FROM project_cors_settings WHERE project_id = $1`,
		projectID,
	).Scan(pq.Array(&origins))
	if errors.Is(err, sql.ErrNoRows) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cors allowlist: %w", err)
	}
	if origins == nil {
		origins = []string{}
	}
	return origins, nil
}

// SetCorsOrigins replaces the project's allowlist. nil clears it. An app keeps
// its claim only on an origin the new list still holds.
func (s *Store) SetCorsOrigins(ctx context.Context, projectID string, origins []string) error {
	if origins == nil {
		origins = []string{}
	}
	_, _, err := s.editCors(ctx, projectID, func(row *corsRow) (bool, error) {
		row.origins = origins
		for app, owned := range row.apps {
			row.apps[app] = slices.DeleteFunc(owned, func(origin string) bool { return !slices.Contains(origins, origin) })
		}
		return true, nil
	})
	return err
}

// corsRow is one project's allowlist and, per app, the origins it added.
type corsRow struct {
	origins []string
	apps    map[string][]string
}

// editCors changes the allowlist under the row's lock, so two edits never
// overwrite each other. edit answers false when nothing changes.
func (s *Store) editCors(ctx context.Context, projectID string, edit func(*corsRow) (bool, error)) (*corsRow, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin cors edit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_cors_settings (project_id) VALUES ($1) ON CONFLICT (project_id) DO NOTHING`, projectID); err != nil {
		return nil, false, fmt.Errorf("create cors row: %w", err)
	}
	row := &corsRow{origins: []string{}, apps: map[string][]string{}}
	var apps []byte
	err = tx.QueryRowContext(ctx,
		`SELECT allowed_origins, app_origins FROM project_cors_settings WHERE project_id = $1 FOR UPDATE`, projectID,
	).Scan(pq.Array(&row.origins), &apps)
	if err != nil {
		return nil, false, fmt.Errorf("lock cors allowlist: %w", err)
	}
	if err := json.Unmarshal(apps, &row.apps); err != nil {
		return nil, false, fmt.Errorf("read app origins: %w", err)
	}
	changed, err := edit(row)
	if err != nil || !changed {
		return row, false, err
	}
	for app, owned := range row.apps {
		if len(owned) == 0 {
			delete(row.apps, app)
		}
	}
	encoded, err := json.Marshal(row.apps)
	if err != nil {
		return nil, false, fmt.Errorf("encode app origins: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE project_cors_settings SET allowed_origins = $2, app_origins = $3, updated_at = $4 WHERE project_id = $1`,
		projectID, pq.Array(row.origins), encoded, time.Now()); err != nil {
		return nil, false, fmt.Errorf("write cors allowlist: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit cors edit: %w", err)
	}
	return row, true, nil
}

// AddCorsOrigin adds one canonical origin. appID, when set, records that the
// app added it, and only when it was not there already.
func (s *Store) AddCorsOrigin(ctx context.Context, projectID, origin, appID string) (bool, []string, error) {
	row, added, err := s.editCors(ctx, projectID, func(row *corsRow) (bool, error) {
		if domain.IsCorsWildcard(row.origins) || slices.Contains(row.origins, origin) {
			return false, nil
		}
		next, err := domain.ParseCorsOrigins(append(slices.Clone(row.origins), origin), false)
		if err != nil {
			return false, err
		}
		row.origins = next
		if appID != "" {
			row.apps[appID] = append(slices.Clone(row.apps[appID]), origin)
		}
		return true, nil
	})
	if err != nil {
		return false, nil, err
	}
	return added, row.origins, nil
}

// RemoveCorsOrigin removes one origin; an app that added it no longer owns it.
func (s *Store) RemoveCorsOrigin(ctx context.Context, projectID, origin string) (bool, []string, error) {
	row, removed, err := s.editCors(ctx, projectID, func(row *corsRow) (bool, error) {
		return removeOrigin(row, origin), nil
	})
	if err != nil {
		return false, nil, err
	}
	return removed, row.origins, nil
}

// ReleaseAppCorsOrigins removes the origins appID added that are still listed,
// and answers them.
func (s *Store) ReleaseAppCorsOrigins(ctx context.Context, projectID, appID string) ([]string, error) {
	var released []string
	_, _, err := s.editCors(ctx, projectID, func(row *corsRow) (bool, error) {
		owned, ok := row.apps[appID]
		if !ok {
			return false, nil
		}
		delete(row.apps, appID)
		for _, origin := range owned {
			if removeOrigin(row, origin) {
				released = append(released, origin)
			}
		}
		return true, nil
	})
	return released, err
}

func removeOrigin(row *corsRow, origin string) bool {
	index := slices.Index(row.origins, origin)
	if index < 0 {
		return false
	}
	row.origins = slices.Delete(slices.Clone(row.origins), index, index+1)
	for app, owned := range row.apps {
		row.apps[app] = slices.DeleteFunc(slices.Clone(owned), func(o string) bool { return o == origin })
	}
	return true
}

var (
	_ storage.ProjectCorsStore  = (*Store)(nil)
	_ storage.ProjectCorsEditor = (*Store)(nil)
)
