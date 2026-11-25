package apphost

import (
	"encoding/json"
	"fmt"
)

var _ SuspensionStore = (*PostgresAppStore)(nil)

// ListSuspendable returns the apps whose pods may be crash-looping.
func (s *PostgresAppStore) ListSuspendable() ([]*App, error) {
	rows, err := s.db.Query(`SELECT doc FROM apps WHERE status IN ($1, $2) ORDER BY project_id, name`, StatusRunning, StatusFailed)
	if err != nil {
		return nil, fmt.Errorf("list suspendable apps: %w", err)
	}
	defer rows.Close()
	out := make([]*App, 0)
	for rows.Next() {
		var blob []byte
		if err := rows.Scan(&blob); err != nil {
			return nil, fmt.Errorf("scan app: %w", err)
		}
		app, err := decodeApp(blob)
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, rows.Err()
}

// RecordSuspension writes or clears the stop inside the stored record without
// touching its version: it is an outcome, not an edit.
func (s *PostgresAppStore) RecordSuspension(projectID, id string, suspension *Suspension) error {
	if err := ValidateProjectID(projectID); err != nil {
		return err
	}
	if err := ValidateID(id); err != nil {
		return err
	}
	var affected int64
	if suspension == nil {
		res, err := s.db.Exec(`UPDATE apps SET doc = doc - 'suspension' WHERE project_id = $1 AND id = $2`, projectID, id)
		if err != nil {
			return fmt.Errorf("clear app suspension: %w", err)
		}
		if affected, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("clear app suspension: %w", err)
		}
	} else {
		blob, err := json.Marshal(suspension)
		if err != nil {
			return fmt.Errorf("marshal app suspension: %w", err)
		}
		res, err := s.db.Exec(`UPDATE apps SET doc = jsonb_set(doc, '{suspension}', $3::jsonb) WHERE project_id = $1 AND id = $2`,
			projectID, id, string(blob))
		if err != nil {
			return fmt.Errorf("record app suspension: %w", err)
		}
		if affected, err = res.RowsAffected(); err != nil {
			return fmt.Errorf("record app suspension: %w", err)
		}
	}
	if affected == 0 {
		return ErrAppNotFound
	}
	return nil
}
