package postgres

import (
	"context"

	"github.com/lib/pq"
)

// HostLogCursors is each followed container's newest stored line (EXC-672).
func (s *Store) HostLogCursors(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT container_id, shipped_until_ns FROM host_log_cursors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cursors := map[string]int64{}
	for rows.Next() {
		var id string
		var ns int64
		if err := rows.Scan(&id, &ns); err != nil {
			return nil, err
		}
		cursors[id] = ns
	}
	return cursors, rows.Err()
}

// SaveHostLogCursor moves a container's cursor forward, never back.
func (s *Store) SaveHostLogCursor(ctx context.Context, containerID, projectID string, shippedUntilNS int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO host_log_cursors (container_id, project_id, shipped_until_ns, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (container_id) DO UPDATE SET
			shipped_until_ns = GREATEST(host_log_cursors.shipped_until_ns, EXCLUDED.shipped_until_ns),
			updated_at = now()`, containerID, projectID, shippedUntilNS)
	return err
}

// PruneHostLogCursors forgets the cursors of containers the engine no longer has.
func (s *Store) PruneHostLogCursors(ctx context.Context, keep []string) error {
	if keep == nil {
		keep = []string{}
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM host_log_cursors WHERE NOT (container_id = ANY($1))`, pq.Array(keep))
	return err
}
