package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"strconv"

	"github.com/go-sql-driver/mysql"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// MySQL logins (ADR 0041 decision 5): the platform's users, created on the
// primary and filed in vault as MySQL records.

// mysqlRoleDatabase is the database each platform user works in.
var mysqlRoleDatabase = map[string]string{roleAuthAdmin: provisioner.MySQLAuthDatabase}

// setupMySQLCredentials creates the platform users with fresh passwords and
// files every login in vault, the owner's among them.
func (s *ProvisioningService) setupMySQLCredentials(ctx context.Context, inst *domain.DatabaseInstance, pc *provisioner.ProvisionContext) error {
	if err := s.assertNoStoredCredentials(inst.ProjectID); err != nil {
		return pc.Fail(err)
	}
	mysql, err := s.mysqlServerOf()
	if err != nil {
		return pc.Fail(err)
	}
	pc.SetStage(domain.StageRoleCreation)
	passwords := provisioner.MySQLPasswords{
		Admin: generatePassword(32), App: generatePassword(32),
		AuthAdmin: generatePassword(32), Watcher: generatePassword(32),
	}
	pc.SetStep("create the platform users")
	if err := mysql.EnsurePlatformUsers(ctx, inst.Namespace, inst.ProjectID, inst.DatabaseName, passwords); err != nil {
		return pc.Fail(err)
	}
	inst.Username = roleAdmin
	for role, password := range map[string]string{
		roleAdmin: passwords.Admin, roleApp: passwords.App, roleAuthAdmin: passwords.AuthAdmin, roleWatcher: passwords.Watcher,
	} {
		pc.SetStep("store " + role + " credentials")
		if err := s.putCredentialRecord(inst.ProjectID, role, mysqlCredentialRecord(inst, role, password), pc); err != nil {
			return pc.Fail(err)
		}
	}
	log.Printf("Created MySQL platform users for %s and stored them in vault", inst.ProjectID)
	return nil
}

// mysqlCredentialRecord is one platform user's login as vault files it.
func mysqlCredentialRecord(inst *domain.DatabaseInstance, role, password string) map[string]string {
	database := inst.DatabaseName
	if own, ok := mysqlRoleDatabase[role]; ok {
		database = own
	}
	return map[string]string{
		domain.CredentialEngineKey: domain.CredentialEngineMySQL,
		domain.CredentialTLSKey:    domain.CredentialTLSRequired,
		"host":                     inst.Host,
		"port":                     strconv.Itoa(mysqlPortOf(inst)),
		"database":                 database,
		"username":                 role,
		"password":                 password,
	}
}

// mysqlCredentials is the owner's login with its MySQL connection strings.
func mysqlCredentials(inst *domain.DatabaseInstance, password string) *domain.CredentialsResponse {
	port := strconv.Itoa(mysqlPortOf(inst))
	return &domain.CredentialsResponse{
		ProjectID:     inst.ProjectID,
		Host:          inst.Host,
		ReadOnlyHost:  inst.ReadOnlyHost,
		Port:          mysqlPortOf(inst),
		DatabaseName:  inst.DatabaseName,
		Username:      inst.Username,
		Password:      password,
		SSLMode:       inst.SSLMode,
		ConnectionURL: domain.MySQLURL(inst.Host, port, inst.Username, password, inst.DatabaseName),
		Engine:        domain.CredentialEngineMySQL,
		JDBCURL:       domain.MySQLJDBCURL(inst.Host, port, inst.DatabaseName),
	}
}

func mysqlPortOf(inst *domain.DatabaseInstance) int {
	if inst.Port != nil {
		return *inst.Port
	}
	return 3306
}

// verifyMySQLUser opens MySQL as the user over TLS and runs a trivial query.
// Errors name the user and nothing else.
func (v *TenantRoleVerifier) verifyMySQLUser(ctx context.Context, inst *domain.DatabaseInstance, username, password string) error {
	connector, err := mysql.NewConnector(mysqlVerifyConfig(inst, username, password, v.overrides))
	if err != nil {
		return fmt.Errorf("open project database as %s: connection could not be established", username)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	var one int
	if err := db.QueryRowContext(ctx, probeSQL).Scan(&one); err != nil {
		return fmt.Errorf("rotated %s credential did not open the project database", username)
	}
	return nil
}

// mysqlVerifyConfig connects as the user over TLS without verifying the
// server certificate, as ssl-mode=REQUIRED does, and names no database: each
// platform user works in its own.
func mysqlVerifyConfig(inst *domain.DatabaseInstance, username, password string, overrides dsnOverrides) *mysql.Config {
	host, port := inst.Host, strconv.Itoa(mysqlPortOf(inst))
	if overrides.host != "" {
		host = overrides.host
	}
	if overrides.port != "" {
		port = overrides.port
	}
	config := mysql.NewConfig()
	config.User, config.Passwd = username, password
	config.Net, config.Addr = "tcp", net.JoinHostPort(host, port)
	config.TLSConfig = "skip-verify"
	return config
}

// reapplyMySQLCredentials sets every filed platform login back to its vault
// password after the database was replaced by a backup of another time.
func (s *ProvisioningService) reapplyMySQLCredentials(ctx context.Context, inst *domain.DatabaseInstance) error {
	filed, err := s.filedCredentialPaths(inst.ProjectID)
	if err != nil {
		return err
	}
	passwords := map[string]string{}
	for _, role := range []string{roleAdmin, roleAuthAdmin, roleApp, roleWatcher} {
		path := vaultCredentialPath(inst.ProjectID, role)
		if !filed[path] {
			continue
		}
		record, err := s.vault.Get(path)
		if err != nil {
			return fmt.Errorf("read %s credential: %w", role, err)
		}
		if record["password"] == "" {
			return fmt.Errorf("the filed %s credential has no password", role)
		}
		passwords[record["username"]] = record["password"]
	}
	if len(passwords) == 0 {
		return nil
	}
	mysql, err := s.mysqlServerOf()
	if err != nil {
		return err
	}
	return mysql.SetUserPasswords(ctx, inst.Namespace, inst.ProjectID, passwords)
}
