package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
		"{{FOREIGN_DEP}}", foreignDependencySQL(),
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

// platformTables are the tables in {{NS}}, and everything attached to one that
// can run code or point elsewhere (its triggers, policies, defaults,
// constraints, indexes and rules), each as the catalog and oid pg_depend knows it by.
const attachedToPlatformTables = `
      SELECT t.oid AS rel, 'pg_class'::regclass AS classid, t.oid AS objid FROM pg_class t WHERE t.relnamespace = {{NS}} AND t.relkind IN ('r', 'p')
      UNION ALL SELECT g.tgrelid, 'pg_trigger'::regclass, g.oid FROM pg_trigger g JOIN pg_class t ON t.oid = g.tgrelid WHERE t.relnamespace = {{NS}}
        AND NOT (g.tgisinternal AND EXISTS (SELECT 1 FROM pg_constraint fk WHERE fk.oid = g.tgconstraint AND fk.contype = 'f' AND fk.confrelid = g.tgrelid AND fk.conrelid <> g.tgrelid))
      UNION ALL SELECT pl.polrelid, 'pg_policy'::regclass, pl.oid FROM pg_policy pl JOIN pg_class t ON t.oid = pl.polrelid WHERE t.relnamespace = {{NS}}
      UNION ALL SELECT a.adrelid, 'pg_attrdef'::regclass, a.oid FROM pg_attrdef a JOIN pg_class t ON t.oid = a.adrelid WHERE t.relnamespace = {{NS}}
      UNION ALL SELECT k.conrelid, 'pg_constraint'::regclass, k.oid FROM pg_constraint k JOIN pg_class t ON t.oid = k.conrelid WHERE t.relnamespace = {{NS}}
      UNION ALL SELECT i.indrelid, 'pg_class'::regclass, i.indexrelid FROM pg_index i JOIN pg_class t ON t.oid = i.indrelid WHERE t.relnamespace = {{NS}}
      UNION ALL SELECT w.ev_class, 'pg_rewrite'::regclass, w.oid FROM pg_rewrite w JOIN pg_class t ON t.oid = w.ev_class WHERE t.relnamespace = {{NS}}`

// taintedTables are tables in {{NS}} that depend on something that is not the
// platform's, or that a customer publication includes. The dependencies are the
// ones pg_depend records for the table and what is attached to it: a trigger's
// function and WHEN clause, a policy's expressions and the relations it names, a
// default, a CHECK, a foreign key's other table, a column's type and a domain's
// own checks, an index's expressions, operator classes and collations.
var taintedTables = `(
    SELECT att.rel FROM (` + attachedToPlatformTables + `
    ) att WHERE EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = att.classid AND d.objid = att.objid AND {{FOREIGN_DEP}})
    UNION SELECT pr.prrelid FROM pg_publication_rel pr JOIN pg_publication pu ON pu.oid = pr.prpubid
      JOIN pg_roles r ON r.oid = pu.pubowner JOIN pg_class t ON t.oid = pr.prrelid WHERE t.relnamespace = {{NS}} AND {{CUSTOMER_R}}
    UNION SELECT t.oid FROM pg_class t JOIN pg_publication_namespace pn ON pn.pnnspid = t.relnamespace
      JOIN pg_publication pu ON pu.oid = pn.pnpubid JOIN pg_roles r ON r.oid = pu.pubowner WHERE t.relnamespace = {{NS}} AND t.relkind IN ('r', 'p') AND {{CUSTOMER_R}}
  )`

// ignoredDependencyCatalogs are the catalogs a dependency can name that are
// nobody's object: the schema, an access method, a language.
var ignoredDependencyCatalogs = []string{"pg_namespace", "pg_am", "pg_language"}

// foreignDependencySQL is true of a pg_depend row d whose referenced object is
// not the platform's: it sits outside the platform schemas, or a customer role
// owns it, unless it belongs to an extension a customer does not own. A
// referenced catalog the repair does not know is foreign too.
// aliasRef matches a table alias used as a qualifier, never the tail of a longer name.
func aliasRef(alias string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\.`)
}

func foreignDependencySQL() string {
	platformSchemas := "(SELECT oid FROM pg_namespace WHERE nspname = ANY (" + textArraySQL(platformSchemaNames) + "))"
	known := make([]string, 0, len(schemaObjectKinds)+len(ignoredDependencyCatalogs))
	parts := make([]string, 0, len(schemaObjectKinds)+1)
	for _, kind := range schemaObjectKinds {
		alias := "ref_" + kind.alias
		known = append(known, "'"+kind.catalog+"'")
		foreign := alias + "." + kind.namespaceColumn + " NOT IN " + platformSchemas
		if kind.owner != "" {
			foreign += " OR EXISTS (SELECT 1 FROM pg_roles dep_owner WHERE dep_owner.oid = " + aliasRef(kind.alias).ReplaceAllString(kind.owner, alias+".") + " AND " + customerRolePredicate("dep_owner") + ")"
		}
		parts = append(parts, "(d.refclassid = '"+kind.catalog+"'::regclass AND EXISTS (SELECT 1 FROM "+kind.catalog+" "+alias+
			" WHERE "+alias+".oid = d.refobjid AND ("+foreign+")))")
	}
	known = append(known, "'pg_constraint'")
	parts = append(parts, `(d.refclassid = 'pg_constraint'::regclass AND EXISTS (SELECT 1 FROM pg_constraint ref_k
        LEFT JOIN pg_class ref_kc ON ref_kc.oid = ref_k.conrelid LEFT JOIN pg_type ref_kt ON ref_kt.oid = ref_k.contypid
        WHERE ref_k.oid = d.refobjid AND ((ref_kc.oid IS NOT NULL AND (ref_kc.relnamespace NOT IN `+platformSchemas+`
          OR EXISTS (SELECT 1 FROM pg_roles dep_owner WHERE dep_owner.oid = ref_kc.relowner AND `+customerRolePredicate("dep_owner")+`)))
          OR (ref_kt.oid IS NOT NULL AND ref_kt.typnamespace NOT IN `+platformSchemas+`))))`)
	for _, catalog := range ignoredDependencyCatalogs {
		known = append(known, "'"+catalog+"'")
	}
	parts = append(parts, "d.refclassid::regclass::text <> ALL (ARRAY["+strings.Join(known, ", ")+"])")
	inExtension := `NOT EXISTS (SELECT 1 FROM pg_depend ext_dep JOIN pg_extension ext ON ext.oid = ext_dep.refobjid JOIN pg_roles ext_owner ON ext_owner.oid = ext.extowner
        WHERE ext_dep.refclassid = 'pg_extension'::regclass AND ext_dep.classid = d.refclassid AND ext_dep.objid = d.refobjid AND ext_dep.deptype = 'e' AND NOT ` + customerRolePredicate("ext_owner") + `)`
	return "((" + strings.Join(parts, "\n      OR ") + ")\n      AND " + inExtension + ")"
}

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
