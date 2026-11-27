package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/pgroles"
	"github.com/excalibase/provisioning-poc/internal/pgscram"
)

// platformServiceSchemas are the platform's schemas whose objects belong to
// one platform role: auth to auth_admin, excalibase_cdc to cdc_watcher. The
// excalibase schema is the superuser's alone and has its own rebuild. tables
// are the tables the platform's own services create there: one of them a
// customer role came to own is taken back in place, never moved. Any other
// relation a customer role owns is left for an operator. A table the auth
// service adds later belongs in this list.
var platformServiceSchemas = []struct {
	schema, serviceRole string
	tables              []string
}{
	{"auth", roleAuthAdmin, []string{"api_keys", "audit_log", "auth_codes", "email_change_tokens", "email_otp_lock", "email_otps",
		"email_verification_tokens", "flow_states", "guest_lock", "guest_sign_ups", "identities", "mfa_challenges", "mfa_factors",
		"mfa_failures", "mfa_recovery_codes", "password_reset_tokens", "refresh_tokens", "role_changes", "schema_migrations", "users"}},
	{"excalibase_cdc", roleWatcher, []string{"excalibase_cdc_slot_owners"}},
}

// platformSchemaLeftMarker is in the refusal's message, and in the error a
// reconcile then reports.
const platformSchemaLeftMarker = "left for an operator"

// platformSchemaNames are the schemas a routine or operator may live in and
// still count as the platform's, with the built-in ones.
var platformSchemaNames = []string{"pg_catalog", "information_schema", "auth", "excalibase", "excalibase_cdc"}

func serviceRolesArray() string {
	var rows []string
	for _, entry := range platformServiceSchemas {
		rows = append(rows, "("+sqlTextLiteral(entry.schema)+", "+sqlTextLiteral(entry.serviceRole)+", "+textArraySQL(entry.tables)+")")
	}
	return strings.Join(rows, ", ")
}

func textArraySQL(values []string) string {
	literals := make([]string, 0, len(values))
	for _, value := range values {
		literals = append(literals, sqlTextLiteral(value))
	}
	return "ARRAY[" + strings.Join(literals, ", ") + "]::text[]"
}

// customerRolePredicate is true for a role customer SQL can act as: any role
// that is not a superuser, not a predefined pg_ role (the server refuses to
// create one) and not one of the exact roles the platform creates. Never a
// prefix: the owner can create excalibase_helper. A role of a platform name
// that a customer administers or granted membership in is the customer's.
// excalibase_app, excalibase_runtime and excalibase_reader are not on the list,
// since customers hold their passwords.
func customerRolePredicate(alias string) string {
	return strings.NewReplacer("{{R}}", alias, "{{NAMES}}", textArraySQL(pgroles.PlatformSchemaRoles())).Replace(
		`(NOT {{R}}.rolsuper AND NOT starts_with({{R}}.rolname, 'pg_') AND ({{R}}.rolname <> ALL ({{NAMES}})
    OR EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles mem ON mem.oid = m.member JOIN pg_roles grantor ON grantor.oid = m.grantor
               WHERE m.roleid = {{R}}.oid AND ((m.admin_option AND NOT mem.rolsuper) OR NOT grantor.rolsuper))))`)
}

// SQL fragments both the repair and the probe read, so they cannot disagree.
func platformSchemaFragments() *strings.Replacer {
	foreignRoutine := func(alias, ownerColumn, namespaceColumn string) string {
		return "(" + alias + "." + namespaceColumn + " NOT IN (SELECT oid FROM pg_namespace WHERE nspname = ANY (" + textArraySQL(platformSchemaNames) + "))" +
			" OR EXISTS (SELECT 1 FROM pg_roles routine_owner WHERE routine_owner.oid = " + alias + "." + ownerColumn +
			" AND " + customerRolePredicate("routine_owner") + "))"
	}
	return strings.NewReplacer(
		"{{SERVICE_ROLES}}", serviceRolesArray(),
		"{{CUSTOMER_R}}", customerRolePredicate("r"),
		"{{CUSTOMER_O}}", customerRolePredicate("o"),
		"{{CUSTOMER_D}}", customerRolePredicate("dr"),
		"{{FOREIGN_PROC}}", foreignRoutine("p", "proowner", "pronamespace"),
		"{{FOREIGN_OP}}", foreignRoutine("op", "oprowner", "oprnamespace"),
		"{{FOREIGN_OPCLASS}}", foreignRoutine("oc", "opcowner", "opcnamespace"),
		"{{TAINTED}}", excalibaseSchemaTaintedCondition,
	)
}

// typeOwnerOf is the owner a type counts as having: a multirange is the owner's
// whose range type it belongs to, since Postgres keeps it with the superuser
// that created it when the range type's owner changes.
func typeOwnerOf(alias string) string {
	return strings.ReplaceAll(`(CASE WHEN {{T}}.typtype = 'm' THEN coalesce((SELECT rt.typowner FROM pg_range g JOIN pg_type rt ON rt.oid = g.rngtypid
      WHERE g.rngmultitypid = {{T}}.oid), {{T}}.typowner) ELSE {{T}}.typowner END)`, "{{T}}", alias)
}

// taintedTables are tables in {{NS}} with a policy, column default, CHECK or
// index expression that calls a routine or operator, or an index that uses an
// operator class, the platform does not own.
const taintedTables = `(
    SELECT x.rel FROM (
      SELECT a.adrelid AS rel, d.refclassid, d.refobjid FROM pg_attrdef a JOIN pg_depend d ON d.classid = 'pg_attrdef'::regclass AND d.objid = a.oid
      UNION ALL SELECT c.conrelid, d.refclassid, d.refobjid FROM pg_constraint c JOIN pg_depend d ON d.classid = 'pg_constraint'::regclass AND d.objid = c.oid WHERE c.conrelid <> 0
      UNION ALL SELECT pl.polrelid, d.refclassid, d.refobjid FROM pg_policy pl JOIN pg_depend d ON d.classid = 'pg_policy'::regclass AND d.objid = pl.oid
      UNION ALL SELECT i.indrelid, d.refclassid, d.refobjid FROM pg_index i JOIN pg_depend d ON d.classid = 'pg_class'::regclass AND d.objid = i.indexrelid
    ) x
    WHERE (x.refclassid = 'pg_proc'::regclass AND EXISTS (SELECT 1 FROM pg_proc p WHERE p.oid = x.refobjid AND {{FOREIGN_PROC}}))
       OR (x.refclassid = 'pg_operator'::regclass AND EXISTS (SELECT 1 FROM pg_operator op WHERE op.oid = x.refobjid AND {{FOREIGN_OP}}))
       OR (x.refclassid = 'pg_opclass'::regclass AND EXISTS (SELECT 1 FROM pg_opclass oc WHERE oc.oid = x.refobjid AND {{FOREIGN_OPCLASS}}))
  )`

// storedRoleVerifier is one role and the verifier pg_authid holds for it.
type storedRoleVerifier struct{ Role, Verifier string }

// passwordDrift names the roles whose stored verifier was not made from the
// password the vault holds, with that password. A role the database does not
// list is left alone; one with no readable verifier counts as drifted.
func passwordDrift(stored []storedRoleVerifier, want []RolePassword) []RolePassword {
	verifiers := make(map[string]string, len(stored))
	for _, entry := range stored {
		verifiers[entry.Role] = entry.Verifier
	}
	var drifted []RolePassword
	for _, role := range want {
		verifier, listed := verifiers[role.Role]
		if listed && !pgscram.Matches(verifier, role.Password) {
			drifted = append(drifted, role)
		}
	}
	return drifted
}

// superuserProbeCommand runs one read-only statement as the superuser with the
// connect-safe options, so an owner's database settings cannot change its answer.
func superuserProbeCommand(dbName, statement string) []string {
	return []string{"psql", "-X", "-tA", "-U", "postgres", "-d", superuserConninfo(dbName, 0), "-c", statement}
}

// Tags that start the probe's two answer lines. A stored verifier line starts
// with a role name, which can never begin with a tilde the platform chose.
const (
	probeAuthTag    = "~excalibase-auth"
	probeSchemasTag = "~excalibase-schemas"
)

// platformState is what one probe of a project's database found.
type platformState struct {
	appReachesAuth bool
	schemasDrifted bool
	stored         []storedRoleVerifier
}

// passwordDriftRoles are the logins customer SQL can run as or hand out, whose
// password it could therefore change with ALTER ROLE ... PASSWORD.
func passwordDriftRoles(owner string) []string {
	return []string{owner, roleApp, roleReader, roleRuntime}
}

// platformProbeSQL asks everything the reconcile checks in one statement: the
// auth access probe, the platform-schema probe and the stored verifiers. One
// call costs far less than three.
func platformProbeSQL(roles []string) string {
	return "SELECT '" + probeAuthTag + "' || E'\\t' || CASE WHEN (" + authAccessProbeSQL + ") THEN 't' ELSE 'f' END\n" +
		"UNION ALL SELECT '" + probeSchemasTag + "' || E'\\t' || CASE WHEN (" + platformSchemaDriftProbeSQL() + ") THEN 't' ELSE 'f' END\n" +
		"UNION ALL SELECT rolname || E'\\t' || coalesce(rolpassword, '') FROM pg_authid WHERE rolname = ANY (" + textArraySQL(roles) + ")"
}

// probePlatform runs the batched probe. An answer missing either flag is an
// error, never a guess.
func probePlatform(ctx context.Context, exec roleSQLExecer, inst *domain.DatabaseInstance) (platformState, error) {
	out, err := exec(ctx, superuserProbeCommand(inst.DatabaseName, platformProbeSQL(passwordDriftRoles(inst.Username))), "")
	if err != nil {
		return platformState{}, fmt.Errorf("probe the project's auth access and platform schemas: %w", withoutVerifiers(err))
	}
	return parsePlatformState(out)
}

func parsePlatformState(out string) (platformState, error) {
	var state platformState
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		role, value, found := strings.Cut(strings.TrimSpace(line), "\t")
		if !found {
			continue
		}
		switch role {
		case probeAuthTag, probeSchemasTag:
			if value != "t" && value != "f" {
				return platformState{}, fmt.Errorf("probe the project's auth access and platform schemas: unexpected answer %q", value)
			}
			seen[role] = true
			if role == probeAuthTag {
				state.appReachesAuth = value == "t"
			} else {
				state.schemasDrifted = value == "t"
			}
		default:
			state.stored = append(state.stored, storedRoleVerifier{Role: role, Verifier: value})
		}
	}
	if !seen[probeAuthTag] || !seen[probeSchemasTag] {
		return platformState{}, errors.New("probe the project's auth access and platform schemas: the answer was incomplete")
	}
	return state, nil
}

// filedPasswords is the password the vault holds for each of roles, left out
// where none is filed, the record is a rotation's pending one, or a rotation
// is in flight (the vault and the database may then differ on purpose).
func (s *ProvisioningService) filedPasswords(projectID string, roles []string, vaultRoles map[string]string) ([]RolePassword, error) {
	filed, err := s.filedCredentialPaths(projectID)
	if err != nil {
		return nil, err
	}
	var passwords []RolePassword
	for _, role := range roles {
		vaultRole := vaultRoles[role]
		if !filed[vaultCredentialPath(projectID, vaultRole)] || filed[vaultPendingCredentialPath(projectID, vaultRole)] {
			continue
		}
		record, err := s.vault.Get(vaultCredentialPath(projectID, vaultRole))
		if err != nil {
			return nil, fmt.Errorf("read %s credential: %w", vaultRole, err)
		}
		if record["password"] == "" || record["state"] == pendingCredentialState {
			continue
		}
		passwords = append(passwords, RolePassword{Role: role, Password: record["password"]})
	}
	return passwords, nil
}

// passwordsDrifted returns the roles whose stored verifier is not the vault's
// password, each with the vault's password, which is what the repair sets back.
func (s *ProvisioningService) passwordsDrifted(inst *domain.DatabaseInstance, stored []storedRoleVerifier) ([]RolePassword, error) {
	roles := passwordDriftRoles(inst.Username)
	vaultRoles := map[string]string{inst.Username: roleAdmin, roleApp: roleApp, roleReader: roleReader, roleRuntime: roleRuntime}
	want, err := s.filedPasswords(inst.ProjectID, roles, vaultRoles)
	if err != nil || len(want) == 0 {
		return nil, err
	}
	return passwordDrift(stored, want), nil
}

// passwordRepairSQL sets each drifted role back to the password the vault
// holds, and only the password: a login the platform switched off stays off.
func passwordRepairSQL(drifted []RolePassword) (string, error) {
	verifiers, err := roleVerifiers(drifted)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, entry := range verifiers {
		fmt.Fprintf(&b, "\nDO $$ BEGIN EXECUTE format('ALTER ROLE %%I PASSWORD %%L', %s, %s); %s END $$;",
			sqlTextLiteral(entry.Role), sqlTextLiteral(entry.Verifier), raiseWithoutContext)
	}
	return b.String(), nil
}
