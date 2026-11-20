package service

import (
	"context"
	"fmt"

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

// reconcileMySQLRuntime creates the runtime login of a MySQL project
// registered before EXC-765 and files it. The platform users are set to the
// passwords vault already holds, so nothing but the new login changes.
func (s *ProvisioningService) reconcileMySQLRuntime(ctx context.Context, projectID string) error {
	inst, release, err := s.holdProject(ctx, projectID, OperationRuntimeLogin, requireActive)
	if err != nil {
		return err
	}
	defer release()
	filed, err := s.filedCredentialPaths(projectID)
	if err != nil {
		return err
	}
	if filed[vaultCredentialPath(projectID, roleRuntime)] {
		return nil
	}
	passwords, serverCA, err := s.filedMySQLPasswords(projectID, filed)
	if err != nil {
		return err
	}
	passwords.Runtime = generatePassword(32)
	server, err := s.mysqlServerOf()
	if err != nil {
		return err
	}
	if err := server.EnsurePlatformUsers(ctx, inst.Namespace, projectID, inst.DatabaseName, passwords); err != nil {
		return fmt.Errorf("create the %s user: %w", roleRuntime, err)
	}
	record := mysqlCredentialRecord(inst, roleRuntime, passwords.Runtime, serverCA)
	if err := s.vault.Put(vaultCredentialPath(projectID, roleRuntime), record); err != nil {
		return fmt.Errorf("file %s credential: %w", roleRuntime, err)
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
		roleWatcher: &passwords.Watcher, roleReader: &passwords.Reader,
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
