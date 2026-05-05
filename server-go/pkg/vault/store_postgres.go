package vault

import (
	"database/sql"
	"fmt"
)

// PostgresStore implements VaultStore using PostgreSQL.
// Tables: vault_barrier (singleton), vault_secrets (path → encrypted blob).
type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) GetBarrier() ([]byte, []byte, error) {
	var barrier, meta []byte
	err := s.db.QueryRow(
		`SELECT encrypted_barrier, meta FROM vault_barrier WHERE id = 1`,
	).Scan(&barrier, &meta)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("get barrier: %w", err)
	}
	return barrier, meta, nil
}

func (s *PostgresStore) PutBarrier(encryptedBarrier []byte, meta []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO vault_barrier (id, encrypted_barrier, meta)
		 VALUES (1, $1, $2)
		 ON CONFLICT (id) DO UPDATE SET encrypted_barrier = EXCLUDED.encrypted_barrier, meta = EXCLUDED.meta`,
		encryptedBarrier, meta,
	)
	if err != nil {
		return fmt.Errorf("put barrier: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetSecret(path string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRow(
		`SELECT entry FROM vault_secrets WHERE path = $1`, path,
	).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get secret: %w", err)
	}
	return data, nil
}

func (s *PostgresStore) PutSecret(path string, data []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO vault_secrets (path, entry) VALUES ($1, $2)
		 ON CONFLICT (path) DO UPDATE SET entry = EXCLUDED.entry`,
		path, data,
	)
	if err != nil {
		return fmt.Errorf("put secret: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteSecret(path string) error {
	_, err := s.db.Exec(`DELETE FROM vault_secrets WHERE path = $1`, path)
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeletePrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, fmt.Errorf("DeletePrefix: empty prefix not allowed")
	}
	res, err := s.db.Exec(`DELETE FROM vault_secrets WHERE path LIKE $1`, prefix+"%")
	if err != nil {
		return 0, fmt.Errorf("delete prefix: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}
	return int(n), nil
}

func (s *PostgresStore) ListSecrets(prefix string) ([]string, error) {
	var rows *sql.Rows
	var err error
	if prefix == "" {
		rows, err = s.db.Query(`SELECT path FROM vault_secrets ORDER BY path`)
	} else {
		rows, err = s.db.Query(`SELECT path FROM vault_secrets WHERE path LIKE $1 ORDER BY path`, prefix+"%")
	}
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan path: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

func (s *PostgresStore) Close() error {
	// Don't close the shared DB connection — it's owned by the platform store.
	return nil
}
