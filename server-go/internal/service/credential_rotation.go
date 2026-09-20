// Package service — credential rotation.
//
// Rotation changes a live database's passwords, so the order of its steps is
// the whole design. The value is recorded durably as PENDING before the
// database is touched, the statement is applied, the new password is proved
// by opening the database with it, and only then does it become the current
// credential. Every intermediate state is one a retry can finish from: either
// the statement never landed and the pending value is rolled back, or it did
// and the pending value is still there to promote.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
)

// rotatedPasswordLength is the length of a rotated role password.
const rotatedPasswordLength = 48

// pendingCredentialState marks a vault record as a rotation in flight. An
// operator reading the vault must be able to tell a credential the database
// accepts from one it may not have taken yet.
const pendingCredentialState = "pending"

// ErrCredentialRotationUnavailable is returned when the platform cannot
// rotate safely: without a vault there is nowhere durable to record the new
// password before the database changes, and without a verifier there is no
// evidence the rotated password works. Refusing is the point — a rotation
// that assumes either is the failure this guards against.
var ErrCredentialRotationUnavailable = errors.New("credential rotation: vault and credential verifier are both required")

// RoleCredentialVerifier proves a password opens a project's database as a
// given role. Implemented by *TenantRoleVerifier; tests substitute their own.
type RoleCredentialVerifier interface {
	VerifyRole(ctx context.Context, inst *domain.DatabaseInstance, username, password string) error
}

// SetCredentialVerifier wires the check that proves a rotated password works.
// Rotation refuses to run without it.
func (s *ProvisioningService) SetCredentialVerifier(v RoleCredentialVerifier) {
	s.credVerifier = v
}

// credentialRotationTarget is one role a rotation replaces.
type credentialRotationTarget struct {
	// role is the vault path suffix under projects/{id}/credentials/.
	role string
	// username is the database role ALTER USER names. It is the vault role
	// name for the platform's own roles and inst.Username for the owner,
	// whose name is chosen per project.
	username string
	// owner marks the credential the API returns and the project row holds.
	owner bool
}

// RotateCredentials replaces the passwords of every platform role filed under
// the project and returns the owner credential.
//
// Roles are rotated one at a time, in a fixed order: auth_admin, then
// excalibase_app, then the owner. The owner is last because it is the value
// the caller is handed and the one stored on the project row — a caller is
// only told a rotation succeeded once every role behind it has been replaced
// and proved. cdc_watcher is deliberately not rotated: its password is baked
// into the deployed watcher workload's configuration, so changing it without
// redeploying that workload stops the project's replication.
func (s *ProvisioningService) RotateCredentials(ctx context.Context, projectID string) (*domain.CredentialsResponse, error) {
	inst, err := s.GetInstance(projectID)
	if err != nil {
		return nil, err
	}
	if s.vault == nil || s.credVerifier == nil {
		return nil, ErrCredentialRotationUnavailable
	}

	targets, err := s.rotationTargets(inst)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if err := s.rotateRoleCredential(ctx, inst, target); err != nil {
			return nil, fmt.Errorf("rotate %s credential: %w", target.role, err)
		}
	}

	s.announceCredentialRotation(ctx, inst.ProjectID)
	return s.GetCredentials(projectID)
}

// rotationTargets lists the roles to replace, in the order they are replaced.
// A platform role with no credential filed in vault is skipped: the platform
// never created it, nothing caches it, and ALTER USER on a role the database
// does not have would fail the whole rotation.
func (s *ProvisioningService) rotationTargets(inst *domain.DatabaseInstance) ([]credentialRotationTarget, error) {
	targets := make([]credentialRotationTarget, 0, 3)
	for _, role := range []string{roleAuthAdmin, roleApp} {
		filed, err := s.credentialIsFiled(inst.ProjectID, role)
		if err != nil {
			return nil, err
		}
		if filed {
			targets = append(targets, credentialRotationTarget{role: role, username: role})
		}
	}
	return append(targets, credentialRotationTarget{
		role: roleAdmin, username: inst.Username, owner: true,
	}), nil
}

// credentialIsFiled reports whether the project has a current credential for
// the role. It asks List rather than Get so a missing entry is not confused
// with a vault that cannot be read — the latter must fail the rotation.
func (s *ProvisioningService) credentialIsFiled(projectID, role string) (bool, error) {
	paths, err := s.vault.List(vaultCredentialPrefix(projectID))
	if err != nil {
		return false, fmt.Errorf("list project credentials: %w", err)
	}
	want := vaultCredentialPath(projectID, role)
	for _, p := range paths {
		if p == want {
			return true, nil
		}
	}
	return false, nil
}

// rotateRoleCredential runs one role through the rotation sequence. It is
// idempotent: a run that finds a pending record left by an interrupted
// attempt adopts that password instead of minting a new one, so repeated
// retries converge on a single value rather than chasing the database with
// a fresh password each time.
func (s *ProvisioningService) rotateRoleCredential(ctx context.Context, inst *domain.DatabaseInstance, target credentialRotationTarget) error {
	pendingPath := vaultPendingCredentialPath(inst.ProjectID, target.role)
	password, resumed := s.resumePendingPassword(pendingPath)
	if !resumed {
		password = generatePassword(rotatedPasswordLength)
		record := roleCredentialRecord(inst, target.username, password)
		record["state"] = pendingCredentialState
		if err := s.vault.Put(pendingPath, record); err != nil {
			return fmt.Errorf("record pending credential: %w", err)
		}
	}

	if err := s.applyAndVerify(ctx, inst, target, password, pendingPath); err != nil {
		return err
	}
	return s.promoteCredential(inst, target, password, pendingPath)
}

// applyAndVerify runs the ALTER and then asks the database whether the new
// password opens it. The two answers together decide what happens to the
// pending record:
//
//   - verified: the rotation continues, whatever the statement reported.
//   - not verified, statement failed: positive evidence that nothing changed
//     and the old password is still live, so the pending record is a residue
//     and is rolled back.
//   - not verified, statement succeeded: the password may well be live and
//     only the check failed. The pending record stays so a retry can finish
//     the rotation, and the caller is told the credential is unproven.
func (s *ProvisioningService) applyAndVerify(ctx context.Context, inst *domain.DatabaseInstance,
	target credentialRotationTarget, password, pendingPath string) error {

	alterErr := s.alterRolePassword(ctx, inst, target.username, password)
	verifyErr := s.credVerifier.VerifyRole(ctx, inst, target.username, password)
	if verifyErr == nil {
		return nil
	}
	if alterErr == nil {
		return fmt.Errorf("verify rotated credential: %w", verifyErr)
	}
	if err := s.vault.Delete(pendingPath); err != nil {
		return fmt.Errorf("alter password: %v; pending credential left in place: %w", alterErr, err)
	}
	return fmt.Errorf("alter password: %w", alterErr)
}

// promoteCredential makes the proved password the current one: the vault copy
// first, then the project row for the owner, and the pending record last. The
// pending record is removed only once everything that reads the credential has
// the new value, so an interruption anywhere in here leaves a retry something
// to finish from.
func (s *ProvisioningService) promoteCredential(inst *domain.DatabaseInstance,
	target credentialRotationTarget, password, pendingPath string) error {

	if err := s.vault.Put(vaultCredentialPath(inst.ProjectID, target.role),
		roleCredentialRecord(inst, target.username, password)); err != nil {
		return fmt.Errorf("promote credential: %w", err)
	}
	if target.owner {
		inst.Password = password
		if err := s.store.Update(inst); err != nil {
			return fmt.Errorf("persist rotated credentials: %w", err)
		}
	}
	if err := s.vault.Delete(pendingPath); err != nil {
		return fmt.Errorf("clear pending credential: %w", err)
	}
	return nil
}

// resumePendingPassword returns the password an interrupted rotation already
// recorded, if there is one. A vault read that fails is treated as "nothing
// pending": the caller then writes a fresh pending record, and a vault that
// cannot be written fails the rotation before the database is touched.
func (s *ProvisioningService) resumePendingPassword(pendingPath string) (string, bool) {
	record, err := s.vault.Get(pendingPath)
	if err != nil || record == nil {
		return "", false
	}
	password := record["password"]
	return password, password != ""
}

// alterRolePassword sets one role's password in the project's database.
// Re-running it with the same password is a no-op, which is what makes a
// retried rotation safe.
//
// The statement carries the password and a failing psql echoes the statement
// it could not run, so the error is scrubbed of the value before it can reach
// a log line or an API response.
func (s *ProvisioningService) alterRolePassword(ctx context.Context, inst *domain.DatabaseInstance, username, password string) error {
	sqlText := fmt.Sprintf("ALTER USER %s PASSWORD %s",
		schema.QuoteIdent(username), schema.QuoteLiteral(password))
	_, err := s.k8sClient.ExecInPod(ctx, inst.Namespace, inst.ProjectID+"-postgres-1", "postgres",
		[]string{"psql", "-U", "postgres", "-c", sqlText})
	if err != nil {
		return errors.New(redactSecret(err.Error(), password))
	}
	return nil
}

// redactedMarker replaces a secret wherever one would otherwise be reported.
const redactedMarker = "[redacted]"

// redactSecret removes every occurrence of secret from text.
func redactSecret(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, redactedMarker)
}

// announceCredentialRotation tells the data plane to drop the credentials it
// caches for the project. It reuses the policy-change subject every other
// cache-invalidating change travels on, so consumers need one subscription.
func (s *ProvisioningService) announceCredentialRotation(ctx context.Context, projectID string) {
	if s.projectEvents == nil {
		return
	}
	s.projectEvents.PublishPolicyChange(ctx, domain.PolicyChangeEvent{
		ProjectID: projectID,
		Kind:      domain.CredentialChangeKind,
		Op:        "rotate",
	})
}

// roleCredentialRecord builds the vault record for a role, in the shape
// provisioning files credentials in.
func roleCredentialRecord(inst *domain.DatabaseInstance, username, password string) map[string]string {
	port := 5432
	if inst.Port != nil {
		port = *inst.Port
	}
	return map[string]string{
		"host":     inst.Host,
		"port":     strconv.Itoa(port),
		"database": inst.DatabaseName,
		"username": username,
		"password": password,
	}
}

// vaultPendingCredentialPath is where a rotation records a password before
// the database is changed. It sits under the project's credential prefix so
// a teardown's prefix delete clears it, and in its own "pending" segment so
// it can never be mistaken for a role's current credential.
func vaultPendingCredentialPath(projectID, role string) string {
	return vaultCredentialPrefix(projectID) + "pending/" + role
}

// TenantRoleVerifier proves a rotated password by opening the project's
// database with it — the only authority on whether an ALTER took effect.
// It honours the same SCHEMA_DB_* overrides the schema and migration paths
// use, so a local port-forward is verified rather than a service name.
type TenantRoleVerifier struct {
	overrides dsnOverrides
	timeout   time.Duration
}

// NewTenantRoleVerifier builds the verifier rotation runs in production.
func NewTenantRoleVerifier() *TenantRoleVerifier {
	return &TenantRoleVerifier{
		overrides: dsnOverrides{
			host:    os.Getenv("SCHEMA_DB_HOST"),
			port:    os.Getenv("SCHEMA_DB_PORT"),
			sslmode: os.Getenv("SCHEMA_DB_SSLMODE"),
		},
		timeout: defaultProbeTimeout,
	}
}

// VerifyRole opens the project's database as username with password and runs
// a trivial query. Errors name the role and nothing else — the connection
// string carries the password being proved.
func (v *TenantRoleVerifier) VerifyRole(ctx context.Context, inst *domain.DatabaseInstance, username, password string) error {
	overrides := v.overrides
	if overrides.sslmode == "" && overrides.host == "" && inst.SSLMode != "" {
		overrides.sslmode = inst.SSLMode
	}
	db, err := sql.Open("postgres", buildTenantDSN(roleCredentialRecord(inst, username, password), overrides))
	if err != nil {
		return fmt.Errorf("open project database as %s: connection could not be established", username)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	var one int
	if err := db.QueryRowContext(ctx, probeSQL).Scan(&one); err != nil {
		return fmt.Errorf("rotated %s credential did not open the project database", username)
	}
	return nil
}
