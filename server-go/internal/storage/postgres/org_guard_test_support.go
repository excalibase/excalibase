package postgres

import "fmt"

// RelaxOrgGuardForTests lets a test seed projects for orgs it never creates, by
// switching off the guard that refuses a project whose org has no row. Test
// harnesses call it once after opening a throwaway database; nothing in the
// server does.
func (s *Store) RelaxOrgGuardForTests() error {
	if _, err := s.db.Exec(`ALTER TABLE database_instances DISABLE TRIGGER database_instances_require_org`); err != nil {
		return fmt.Errorf("relax the org guard: %w", err)
	}
	return nil
}
