package service

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// OperationRuntimeLogin is the lease the startup reconcile holds on a MySQL
// project while it creates the apps' login there (EXC-765).
const OperationRuntimeLogin ProjectOperation = "runtime login reconcile"

// reconcilesMySQLRuntime is a running MySQL project with a database.
func reconcilesMySQLRuntime(inst *domain.DatabaseInstance) bool {
	return inst != nil && inst.DBType == domain.MySQL && !inst.NoDatabase &&
		inst.Status == string(domain.StatusActive)
}

// mysqlGrantsKey and mysqlGrantsEscaped mark an owner record whose project's
// database grants name the database escaped (EXC-765): before that, a name
// holding _ or % was a pattern and reached other databases, auth among them.
const (
	mysqlGrantsKey     = "grants"
	mysqlGrantsEscaped = "escaped"
)

// mysqlGrantsPending reports a MySQL project that needs the platform users
// set again: no runtime login while it is on, or grants made before they were escaped.
func (s *ProvisioningService) mysqlGrantsPending(ctx context.Context, inst *domain.DatabaseInstance) (bool, error) {
	unfiled, err := s.missingRuntimeLogin(ctx, inst.ProjectID)
	if err != nil || unfiled {
		return unfiled, err
	}
	if !strings.ContainsAny(inst.DatabaseName, "_%") {
		return false, nil
	}
	owner, err := s.vault.Get(vaultCredentialPath(inst.ProjectID, roleAdmin))
	if err != nil {
		return false, fmt.Errorf("read %s credential: %w", roleAdmin, err)
	}
	return owner[mysqlGrantsKey] != mysqlGrantsEscaped, nil
}

// reconcileMySQLRuntime sets a MySQL project's platform users again: it
// creates the runtime login of a project registered before EXC-765 and files
// it, and replaces grants a wildcard in the database name made too wide. The
// users keep the passwords vault holds for them.
func (s *ProvisioningService) reconcileMySQLRuntime(ctx context.Context, projectID string) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationRuntimeLogin, requireActive)
	if err != nil {
		return err
	}
	defer release()
	if pending, err := s.mysqlGrantsPending(ctx, inst); err != nil || !pending {
		return err
	}
	filed, err := s.filedCredentialPaths(projectID)
	if err != nil {
		return err
	}
	passwords, serverCA, err := s.filedMySQLPasswords(projectID, filed)
	if err != nil {
		return err
	}
	minted, err := s.mintsMySQLRuntime(ctx, projectID, filed, &passwords)
	if err != nil {
		return err
	}
	server, err := s.mysqlServerOf()
	if err != nil {
		return err
	}
	if err := server.EnsurePlatformUsers(ctx, inst.Namespace, projectID, inst.DatabaseName, passwords); err != nil {
		return fmt.Errorf("set the platform users: %w", err)
	}
	if minted {
		record := mysqlCredentialRecord(inst, roleRuntime, passwords.Runtime, serverCA)
		if err := s.vault.Put(vaultCredentialPath(projectID, roleRuntime), record); err != nil {
			return fmt.Errorf("file %s credential: %w", roleRuntime, err)
		}
	}
	return s.markMySQLGrantsEscaped(projectID)
}

// mintsMySQLRuntime gives a project with no runtime login a password while
// the feature is on; one already filed was read with the others.
func (s *ProvisioningService) mintsMySQLRuntime(ctx context.Context, projectID string, filed map[string]bool, passwords *provisioner.MySQLPasswords) (bool, error) {
	if filed[vaultCredentialPath(projectID, roleRuntime)] {
		return false, nil
	}
	unfiled, err := s.missingRuntimeLogin(ctx, projectID)
	if err != nil || !unfiled {
		return false, err
	}
	passwords.Runtime = generatePassword(32)
	return true, nil
}

// markMySQLGrantsEscaped records on the owner's login that the grants are escaped.
func (s *ProvisioningService) markMySQLGrantsEscaped(projectID string) error {
	path := vaultCredentialPath(projectID, roleAdmin)
	record, err := s.vault.Get(path)
	if err != nil {
		return fmt.Errorf("read %s credential: %w", roleAdmin, err)
	}
	marked := maps.Clone(record)
	marked[mysqlGrantsKey] = mysqlGrantsEscaped
	if err := s.vault.Put(path, marked); err != nil {
		return fmt.Errorf("mark %s credential: %w", roleAdmin, err)
	}
	return nil
}

// filedMySQLPasswords reads the platform users' passwords from vault, with the
// server CA their records carry. A user with none filed stays locked.
func (s *ProvisioningService) filedMySQLPasswords(projectID string, filed map[string]bool) (provisioner.MySQLPasswords, string, error) {
	var passwords provisioner.MySQLPasswords
	var serverCA string
	for role, target := range map[string]*string{
		roleAdmin: &passwords.Admin, roleApp: &passwords.App, roleAuthAdmin: &passwords.AuthAdmin,
		roleWatcher: &passwords.Watcher, roleReader: &passwords.Reader, roleRuntime: &passwords.Runtime,
	} {
		path := vaultCredentialPath(projectID, role)
		if !filed[path] {
			continue
		}
		record, err := s.vault.Get(path)
		if err != nil {
			return passwords, "", fmt.Errorf("read %s credential: %w", role, err)
		}
		*target = record["password"]
		if serverCA == "" {
			serverCA = record[domain.CredentialServerCAKey]
		}
	}
	if passwords.Admin == "" {
		return passwords, "", fmt.Errorf("the project's owner login has no password filed")
	}
	return passwords, serverCA, nil
}

// clearMySQLGrantsMark has the next reconcile pass set the platform users'
// grants again, as after a database was replaced by an older backup's.
func (s *ProvisioningService) clearMySQLGrantsMark(projectID string) error {
	path := vaultCredentialPath(projectID, roleAdmin)
	record, err := s.vault.Get(path)
	if err != nil {
		return fmt.Errorf("read %s credential: %w", roleAdmin, err)
	}
	if _, marked := record[mysqlGrantsKey]; !marked {
		return nil
	}
	cleared := maps.Clone(record)
	delete(cleared, mysqlGrantsKey)
	if err := s.vault.Put(path, cleared); err != nil {
		return fmt.Errorf("clear the grants mark of %s credential: %w", roleAdmin, err)
	}
	return nil
}
