package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/pgscram"
)

// platformServiceSchemas are the platform's schemas whose objects belong to
// one platform role: auth to auth_admin, excalibase_cdc to cdc_watcher. The
// excalibase schema is the superuser's alone and has its own rebuild.
var platformServiceSchemas = []struct{ schema, serviceRole string }{
	{"auth", roleAuthAdmin},
	{"excalibase_cdc", roleWatcher},
}

// customerRolesSQL is the roles customer SQL can run as, as a SQL array of
// literals: excalibase_app, and the project's owner login.
func customerRolesSQL(owner string) string {
	return "ARRAY[" + sqlTextLiteral(roleApp) + ", " + sqlTextLiteral(owner) + "]::text[]"
}

// platformSchemaRepairSQL puts the platform's schemas back in the platform's
// hands, and is safe to run on a clean project (EXC-821). A schema a customer
// role owns, or holds anything on, goes back to the superuser; what such a role
// owns inside auth or excalibase_cdc goes to the schema's service role; a
// trigger a customer role's function runs on a table there is dropped, since
// it would run as whoever writes the row. The excalibase schema is the rebuild's.
func platformSchemaRepairSQL(owner string) string {
	return strings.NewReplacer(
		"{{CUSTOMER_ROLES}}", customerRolesSQL(owner),
		"{{SERVICE_ROLES}}", serviceRolesArray(),
	).Replace(platformSchemaRepairTemplate)
}

func serviceRolesArray() string {
	var pairs []string
	for _, entry := range platformServiceSchemas {
		pairs = append(pairs, "("+sqlTextLiteral(entry.schema)+", "+sqlTextLiteral(entry.serviceRole)+")")
	}
	return strings.Join(pairs, ", ")
}

const platformSchemaRepairTemplate = `
DO $excalibase_schema_repair$
DECLARE
  customer_roles CONSTANT text[] := {{CUSTOMER_ROLES}};
  entry record;
  object_row record;
  customer_role text;
BEGIN
  FOR entry IN SELECT * FROM (VALUES {{SERVICE_ROLES}}) AS v(schema_name, service_role) LOOP
    IF to_regnamespace(entry.schema_name) IS NULL OR to_regrole(entry.service_role) IS NULL THEN
      CONTINUE;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
               WHERE n.nspname = entry.schema_name AND NOT r.rolsuper AND r.rolname <> entry.service_role) THEN
      EXECUTE format('ALTER SCHEMA %I OWNER TO CURRENT_USER', entry.schema_name);
    END IF;
    FOREACH customer_role IN ARRAY customer_roles LOOP
      IF to_regrole(customer_role) IS NULL THEN
        CONTINUE;
      END IF;
      EXECUTE format('REVOKE ALL ON SCHEMA %I FROM %I', entry.schema_name, customer_role);
    END LOOP;
    FOR object_row IN
      SELECT c.oid::regclass::text AS name FROM pg_class c
      JOIN pg_roles r ON r.oid = c.relowner
      WHERE c.relnamespace = to_regnamespace(entry.schema_name) AND r.rolname = ANY (customer_roles)
        AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
    LOOP
      EXECUTE format('ALTER TABLE %s OWNER TO %I', object_row.name, entry.service_role);
      -- A revoke made while the customer role owned it may have taken the owner's own privileges.
      EXECUTE format('GRANT ALL ON TABLE %s TO %I', object_row.name, entry.service_role);
    END LOOP;
    FOR object_row IN
      SELECT p.oid::regprocedure::text AS name FROM pg_proc p
      JOIN pg_roles r ON r.oid = p.proowner
      WHERE p.pronamespace = to_regnamespace(entry.schema_name) AND r.rolname = ANY (customer_roles)
    LOOP
      EXECUTE format('ALTER ROUTINE %s OWNER TO %I', object_row.name, entry.service_role);
    END LOOP;
    FOR object_row IN
      SELECT t.tgname, t.tgrelid::regclass::text AS relation FROM pg_trigger t
      JOIN pg_class c ON c.oid = t.tgrelid
      JOIN pg_proc p ON p.oid = t.tgfoid
      JOIN pg_roles r ON r.oid = p.proowner
      WHERE c.relnamespace = to_regnamespace(entry.schema_name) AND NOT t.tgisinternal
        AND r.rolname = ANY (customer_roles)
    LOOP
      EXECUTE format('DROP TRIGGER %I ON %s', object_row.tgname, object_row.relation);
    END LOOP;
  END LOOP;
END
$excalibase_schema_repair$;
`

// platformSchemaDriftProbeSQL answers t while the platform's schemas are not as
// registration leaves them: the excalibase schema is tainted, a customer role
// holds CREATE or USAGE on, owns or has planted something in auth or
// excalibase_cdc, or a schema there has a customer owner (EXC-821).
func platformSchemaDriftProbeSQL(owner string) string {
	return strings.NewReplacer(
		"{{CUSTOMER_ROLES}}", customerRolesSQL(owner),
		"{{TAINTED}}", excalibaseSchemaTaintedCondition,
		"{{SERVICE_ROLES}}", serviceRolesArray(),
	).Replace(platformSchemaDriftProbeTemplate)
}

const platformSchemaDriftProbeTemplate = `SELECT COALESCE(
  (to_regnamespace('excalibase') IS NOT NULL AND {{TAINTED}})
  OR EXISTS (
    SELECT 1 FROM (VALUES {{SERVICE_ROLES}}) AS v(schema_name, service_role)
    CROSS JOIN unnest({{CUSTOMER_ROLES}}) AS customer(role_name)
    WHERE to_regnamespace(v.schema_name) IS NOT NULL AND to_regrole(customer.role_name) IS NOT NULL
      AND (
        EXISTS (SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
                WHERE n.nspname = v.schema_name AND NOT r.rolsuper AND r.rolname <> v.service_role)
        OR has_schema_privilege(customer.role_name, v.schema_name, 'CREATE')
        OR has_schema_privilege(customer.role_name, v.schema_name, 'USAGE')
        OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
                   WHERE c.relnamespace = to_regnamespace(v.schema_name) AND r.rolname = customer.role_name
                     AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S'))
        OR EXISTS (SELECT 1 FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner
                   WHERE p.pronamespace = to_regnamespace(v.schema_name) AND r.rolname = customer.role_name)
        OR EXISTS (SELECT 1 FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
                   JOIN pg_proc p ON p.oid = t.tgfoid JOIN pg_roles r ON r.oid = p.proowner
                   WHERE c.relnamespace = to_regnamespace(v.schema_name) AND NOT t.tgisinternal
                     AND r.rolname = customer.role_name)
      )
  ), false)`

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

// platformSchemasDrifted asks the database whether the platform's schemas are
// as registration leaves them. An answer that is neither t nor f is an error.
func platformSchemasDrifted(ctx context.Context, exec roleSQLExecer, dbName, owner string) (bool, error) {
	out, err := exec(ctx, superuserProbeCommand(dbName, platformSchemaDriftProbeSQL(owner)), "")
	if err != nil {
		return false, fmt.Errorf("probe the platform schemas: %w", withoutVerifiers(err))
	}
	switch strings.TrimSpace(out) {
	case "t":
		return true, nil
	case "f":
		return false, nil
	}
	return false, fmt.Errorf("probe the platform schemas: unexpected answer %q", strings.TrimSpace(out))
}

// passwordDriftRoles are the logins customer SQL can run as or hand out, whose
// password it could therefore change with ALTER ROLE ... PASSWORD.
func passwordDriftRoles(owner string) []string {
	return []string{owner, roleApp, roleReader, roleRuntime}
}

// storedVerifiersSQL lists the verifier Postgres holds for each of roles.
func storedVerifiersSQL(roles []string) string {
	literals := make([]string, 0, len(roles))
	for _, role := range roles {
		literals = append(literals, sqlTextLiteral(role))
	}
	return "SELECT rolname || E'\\t' || coalesce(rolpassword, '') FROM pg_authid WHERE rolname = ANY (ARRAY[" +
		strings.Join(literals, ", ") + "]::text[])"
}

// parseStoredVerifiers reads the probe's role-tab-verifier lines.
func parseStoredVerifiers(out string) []storedRoleVerifier {
	var stored []storedRoleVerifier
	for _, line := range strings.Split(out, "\n") {
		role, verifier, found := strings.Cut(strings.TrimSpace(line), "\t")
		if found && role != "" {
			stored = append(stored, storedRoleVerifier{Role: role, Verifier: verifier})
		}
	}
	return stored
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

// driftedPasswords returns the roles whose password was changed in SQL, each
// with the password the vault holds, which is what the repair sets back.
func (s *ProvisioningService) driftedPasswords(ctx context.Context, exec roleSQLExecer, inst *domain.DatabaseInstance) ([]RolePassword, error) {
	roles := passwordDriftRoles(inst.Username)
	vaultRoles := map[string]string{inst.Username: roleAdmin, roleApp: roleApp, roleReader: roleReader, roleRuntime: roleRuntime}
	want, err := s.filedPasswords(inst.ProjectID, roles, vaultRoles)
	if err != nil || len(want) == 0 {
		return nil, err
	}
	out, err := exec(ctx, superuserProbeCommand(inst.DatabaseName, storedVerifiersSQL(roles)), "")
	if err != nil {
		return nil, fmt.Errorf("read the stored password verifiers: %w", withoutVerifiers(err))
	}
	return passwordDrift(parseStoredVerifiers(out), want), nil
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

// passwordRepairFor is the SQL that sets back every password changed in SQL.
// It reads the vault and the database under the project's lease, so a rotation
// that finished since the probe is not mistaken for drift.
func (s *ProvisioningService) passwordRepairFor(ctx context.Context, exec roleSQLExecer, inst *domain.DatabaseInstance) (string, error) {
	drifted, err := s.driftedPasswords(ctx, exec, inst)
	if err != nil {
		return "", err
	}
	return passwordRepairSQL(drifted)
}
