//go:build integration

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-635 on a real engine: a single host's MySQL project restored into a new
// project keeps its end users, and nothing the source issued them opens the
// copy. The tables are excalibase-auth's MySQL migration's.
//
// Run: go test -tags=integration ./internal/service/ -run TestHostMySQLRestoredCopy -v
func TestHostMySQLRestoredCopyHasNoSessionsFromTheSource(t *testing.T) {
	docker, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{})
	if err != nil {
		t.Skipf("no docker daemon available: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	suffix := time.Now().UnixNano() % 1_000_000
	source, copyID := fmt.Sprintf("it-mss-%d", suffix), fmt.Sprintf("it-msc-%d", suffix)
	mysqlProv := provisioner.NewDockerMySQLProvisioner(docker)
	plan := config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}
	result, err := mysqlProv.Provision(ctx, domain.ProvisioningRequest{ProjectName: source, DBType: domain.MySQL, DatabaseName: "shop"}, plan, func(domain.ProvisioningStage) {})
	for _, project := range []string{source, copyID} {
		t.Cleanup(func() { _ = mysqlProv.RemoveData(context.Background(), project) })
	}
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	passwords := provisioner.MySQLPasswords{Admin: "Adm1n-" + source, App: "app-" + source, AuthAdmin: "auth-" + source, Watcher: "cdc-" + source}
	if err := mysqlProv.EnsurePlatformUsers(ctx, result.Namespace, source, "shop", passwords); err != nil {
		t.Fatalf("EnsurePlatformUsers: %v", err)
	}
	inst := &domain.DatabaseInstance{ProjectID: source, OrgID: "org1", DBType: domain.MySQL, DeploymentMode: domain.ModeDocker,
		Tier: domain.Standard, Namespace: result.Namespace, Host: result.Host, DatabaseName: "shop", MySQLVersion: config.DefaultMySQLVersion()}
	instances := emptyInstanceStore(t)
	if err := instances.Create(inst); err != nil {
		t.Fatal(err)
	}
	// The copy is registered the way production registers it: real users,
	// real vault records.
	vault := newFakeVault()
	registrar := NewProvisioningService(instances, provisioner.NewFactory(mysqlProv), nil)
	registrar.SetOrgStore(testOrgs())
	setOrgTier(registrar, "org1", domain.Standard)
	registrar.SetVault(vault)
	adapter := NewHostMySQLBackupAdapter(HostMySQLBackupAdapterConfig{
		Docker: docker, MySQL: mysqlProv, Streamer: NewDockerStreamer(docker.RawClient()), Uploader: newFakeS3Uploader(),
		Records: &fakeBackupRecordStore{}, Bucket: "it", KeyPrefix: "backups/", Instances: instances,
	})
	adapter.SetProjectRegistrar(registrar)
	adapter.SetDatabaseProbe(alwaysAnswers{})
	adapter.SetRestorePlanSource(&fakeRestorePlans{plan: RestorePlan{Tier: domain.Standard, Config: plan}})

	auth := hostMySQLAs(ctx, t, docker, result.Namespace, provisioner.MySQLUserAuthAdmin, passwords.AuthAdmin, provisioner.MySQLAuthDatabase)
	execAll(ctx, t, auth, sourceMySQLEndUserAuth...)
	_ = auth.Close()
	if _, err := adapter.TriggerManual(ctx, inst); err != nil {
		t.Fatalf("backup: %v", err)
	}
	restored, err := adapter.Restore(ctx, inst, domain.RestoreRequest{NewProjectName: copyID, TargetProjectID: copyID})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	login, err := vault.Get(vaultCredentialPath(copyID, roleAuthAdmin))
	if err != nil {
		t.Fatalf("the copy's auth_admin login: %v", err)
	}
	copyAuth := hostMySQLAs(ctx, t, docker, restored.Host, provisioner.MySQLUserAuthAdmin, login["password"], provisioner.MySQLAuthDatabase)
	defer copyAuth.Close()
	sourceAuth := hostMySQLAs(ctx, t, docker, result.Namespace, provisioner.MySQLUserAuthAdmin, passwords.AuthAdmin, provisioner.MySQLAuthDatabase)
	defer sourceAuth.Close()
	for credential, lookup := range sourceMySQLCredentialLookups {
		var inCopy, inSource int
		if err := copyAuth.QueryRowContext(ctx, lookup).Scan(&inCopy); err != nil {
			t.Fatalf("%s in the copy: %v", credential, err)
		}
		if err := sourceAuth.QueryRowContext(ctx, lookup).Scan(&inSource); err != nil {
			t.Fatalf("%s in the source: %v", credential, err)
		}
		if inCopy != 0 || inSource != 1 {
			t.Errorf("the source's %s: %d in the copy, %d in the source; want 0 and 1", credential, inCopy, inSource)
		}
	}
	var email string
	if err := copyAuth.QueryRowContext(ctx, "SELECT email FROM users").Scan(&email); err != nil || email != "ada@example.com" {
		t.Errorf("the copy's end users = %q (%v), want the source's", email, err)
	}
}

var sourceMySQLEndUserAuth = []string{
	`CREATE TABLE users (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, email VARCHAR(100) NOT NULL, password VARCHAR(255) NOT NULL,
		full_name VARCHAR(100) NOT NULL, role VARCHAR(63) NOT NULL DEFAULT 'user', allowed_roles JSON NULL, enabled BOOLEAN NOT NULL DEFAULT TRUE,
		email_verified BOOLEAN NOT NULL DEFAULT FALSE, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), last_login_at DATETIME(6) NULL, CONSTRAINT uq_users_email UNIQUE (email))`,
	`CREATE TABLE refresh_tokens (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, token_hash CHAR(64) NOT NULL, family_id CHAR(36) NOT NULL,
		user_id BIGINT NOT NULL, expiry_date DATETIME(6) NOT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		revoked BOOLEAN NOT NULL DEFAULT FALSE, CONSTRAINT uq_refresh_tokens_token_hash UNIQUE (token_hash),
		CONSTRAINT fk_refresh_tokens_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE)`,
	`CREATE TABLE api_keys (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, key_hash VARCHAR(64) NOT NULL, key_prefix VARCHAR(24) NOT NULL,
		key_type VARCHAR(16) NOT NULL, name VARCHAR(100) NULL, created_by BIGINT NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		last_used_at DATETIME(6) NULL, revoked_at DATETIME(6) NULL, revoked_by BIGINT NULL, CONSTRAINT uq_api_keys_key_hash UNIQUE (key_hash))`,
	`CREATE TABLE email_verification_tokens (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, token_hash CHAR(64) NOT NULL, user_id BIGINT NOT NULL,
		expires_at DATETIME(6) NOT NULL, consumed_at DATETIME(6) NULL, confirms_password BOOLEAN NOT NULL DEFAULT FALSE,
		created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6), CONSTRAINT uq_evt_token_hash UNIQUE (token_hash),
		CONSTRAINT fk_evt_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE)`,
	`CREATE TABLE password_reset_tokens (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, token_hash CHAR(64) NOT NULL, user_id BIGINT NOT NULL,
		expires_at DATETIME(6) NOT NULL, consumed_at DATETIME(6) NULL, created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		CONSTRAINT uq_prt_token_hash UNIQUE (token_hash), CONSTRAINT fk_prt_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE)`,
	`INSERT INTO users (email, password, full_name) VALUES ('ada@example.com', 'argon2-hash', 'Ada')`,
	`INSERT INTO refresh_tokens (token_hash, family_id, user_id, expiry_date)
		VALUES (SHA2('source-refresh-token', 256), UUID(), 1, UTC_TIMESTAMP(6) + INTERVAL 30 DAY)`,
	`INSERT INTO api_keys (key_hash, key_prefix, key_type) VALUES (SHA2('source-secret-key', 256), 'esk_source', 'secret')`,
	`INSERT INTO email_verification_tokens (token_hash, user_id, expires_at) VALUES (SHA2('source-verify', 256), 1, UTC_TIMESTAMP(6) + INTERVAL 1 DAY)`,
	`INSERT INTO password_reset_tokens (token_hash, user_id, expires_at) VALUES (SHA2('source-reset', 256), 1, UTC_TIMESTAMP(6) + INTERVAL 1 HOUR)`,
}

// What excalibase-auth looks each presented credential up with.
var sourceMySQLCredentialLookups = map[string]string{
	"refresh token": `SELECT COUNT(*) FROM refresh_tokens
		WHERE token_hash = SHA2('source-refresh-token', 256) AND revoked = FALSE AND expiry_date > UTC_TIMESTAMP(6)`,
	"secret key":          `SELECT COUNT(*) FROM api_keys WHERE key_hash = SHA2('source-secret-key', 256) AND revoked_at IS NULL`,
	"verification link":   `SELECT COUNT(*) FROM email_verification_tokens WHERE token_hash = SHA2('source-verify', 256)`,
	"password reset link": `SELECT COUNT(*) FROM password_reset_tokens WHERE token_hash = SHA2('source-reset', 256)`,
}
