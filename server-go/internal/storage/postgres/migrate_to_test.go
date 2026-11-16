//go:build integration

package postgres

// migrateTo moves the schema to version on a throwaway migrator, for tests
// that need the database as it was before a migration.
func (s *Store) migrateTo(version uint) error {
	m, err := newMigrator(s.databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()
	return m.Migrate(version)
}
