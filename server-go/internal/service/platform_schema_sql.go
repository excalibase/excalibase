package service

import (
	"strconv"
	"strings"
)

// The repair of the platform's schemas runs as three scripts, each its own
// transaction, so a step that refuses or collides cannot undo the ones before
// it (EXC-821): the lockdown, which cannot refuse; the quarantine of routines,
// types and operators; and the refusal for what only an operator can fix.
// It never adopts what a customer role made.

func platformSchemaScripts() (lockdown, quarantine, refusal string) {
	fill := func(template string) string {
		return platformSchemaFragments().Replace(schemaScoped("schema_oid").Replace(template))
	}
	return fill(platformSchemaLockdownTemplate), fill(platformSchemaQuarantineTemplate), fill(platformSchemaRefusalTemplate)
}

// schemaScoped fills the fragments that depend on which schema is read.
func schemaScoped(namespace string) *strings.Replacer {
	scoped := strings.NewReplacer("{{NS}}", namespace, "{{QNS}}", "to_regnamespace(quarantine)")
	return strings.NewReplacer("{{OWNED}}", scoped.Replace(customerOwnedObjects), "{{TAINTED_TABLES}}", taintedTables,
		"{{LAST_TRY}}", strconv.Itoa(quarantineNameTries-1), "{{GRANTED}}", scoped.Replace(grantedOnSchema), "{{REVOKE_HELPER}}", revokeHelperSQL,
		"{{STAGE_LOCKDOWN}}", stageNotice("lockdown"), "{{STAGE_QUARANTINE}}", stageNotice("quarantine"), "{{STAGE_REFUSAL}}", stageNotice("refusal"), "{{MOVABLE}}", scoped.Replace(movableObjects), "{{MOVABLE_IN_QUARANTINE}}", scoped.Replace(movableWithTargets))
}

// The lockdown takes the schema back, reclaims the platform's own tables a
// customer role owns (the known set, never moved), drops triggers and rules the
// platform did not put on its tables, and revokes every grant a customer role
// holds and PUBLIC's EXECUTE. Nothing in it can refuse or collide.
const platformSchemaLockdownTemplate = `
SET LOCAL statement_timeout = '60s';
{{STAGE_LOCKDOWN}}
{{REVOKE_HELPER}}
DO $excalibase_schema_lockdown$
DECLARE
  entry record;
  item record;
  schema_oid oid;
BEGIN
  FOR entry IN SELECT * FROM (VALUES {{SERVICE_ROLES}}) AS v(schema_name, service_role, known_tables) LOOP
    schema_oid := to_regnamespace(entry.schema_name);
    IF schema_oid IS NULL OR to_regrole(entry.service_role) IS NULL THEN
      CONTINUE;
    END IF;

    IF EXISTS (SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
               WHERE n.oid = schema_oid AND {{CUSTOMER_R}}) THEN
      EXECUTE format('ALTER SCHEMA %I OWNER TO CURRENT_USER', entry.schema_name);
    END IF;

    FOR item IN
      SELECT c.oid::regclass::text AS name FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
      WHERE c.relnamespace = schema_oid AND c.relkind IN ('r', 'p') AND c.relname = ANY (entry.known_tables) AND {{CUSTOMER_R}}
    LOOP
      EXECUTE format('ALTER TABLE %s OWNER TO %I', item.name, entry.service_role);
    END LOOP;

    FOR item IN
      SELECT t.tgname, t.tgrelid::regclass::text AS relation FROM pg_trigger t
      JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_roles r ON r.oid = c.relowner
      WHERE c.relnamespace = schema_oid AND NOT t.tgisinternal AND NOT {{CUSTOMER_R}}
        AND NOT EXISTS (SELECT 1 FROM pg_proc p WHERE p.oid = t.tgfoid AND NOT {{FOREIGN_PROC}})
    LOOP
      EXECUTE format('DROP TRIGGER %I ON %s', item.tgname, item.relation);
    END LOOP;
    FOR item IN
      SELECT w.rulename, w.ev_class::regclass::text AS relation FROM pg_rewrite w
      JOIN pg_class c ON c.oid = w.ev_class JOIN pg_roles r ON r.oid = c.relowner
      WHERE c.relnamespace = schema_oid AND w.rulename <> '_RETURN' AND NOT {{CUSTOMER_R}}
    LOOP
      EXECUTE format('DROP RULE %I ON %s', item.rulename, item.relation);
    END LOOP;

    FOR item IN
      SELECT g.kind, g.ident, array_agg(DISTINCT r.rolname) AS roles FROM ({{GRANTED}}) g
      JOIN pg_roles r ON r.oid = g.grantee WHERE {{CUSTOMER_R}} GROUP BY g.kind, g.ident
    LOOP
      PERFORM pg_temp.excalibase_revoke('ALL', item.kind, item.ident, item.roles);
    END LOOP;
    FOR item IN
      SELECT d.defaclrole::regrole::text AS owner_role, d.defaclnamespace = 0 AS schemaless,
             CASE d.defaclobjtype WHEN 'r' THEN 'TABLES' WHEN 'S' THEN 'SEQUENCES' WHEN 'f' THEN 'FUNCTIONS' ELSE 'TYPES' END AS kind,
             string_agg(quote_ident(r.rolname), ', ') AS roles
      FROM pg_default_acl d CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles r ON r.oid = a.grantee JOIN pg_roles dr ON dr.oid = d.defaclrole
      WHERE (d.defaclnamespace = schema_oid OR (d.defaclnamespace = 0 AND NOT {{CUSTOMER_D}}))
        AND d.defaclobjtype IN ('r', 'S', 'f', 'T') AND {{CUSTOMER_R}}
      GROUP BY d.defaclrole, d.defaclnamespace, d.defaclobjtype
    LOOP
      EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %s %s REVOKE ALL ON %s FROM %s CASCADE', item.owner_role,
        CASE WHEN item.schemaless THEN '' ELSE format('IN SCHEMA %I', entry.schema_name) END, item.kind, item.roles);
    END LOOP;
    EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC CASCADE', entry.schema_name);
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %I FROM PUBLIC CASCADE', entry.schema_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA %I FROM PUBLIC CASCADE', entry.schema_name);
    EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %I FROM PUBLIC CASCADE', entry.schema_name);
  END LOOP;
END
$excalibase_schema_lockdown$;
`

// The quarantine moves a customer's routines, types, domains and operators out
// of the platform's schema, their owner unchanged. The quarantine schema is
// taken over when it exists (owner to the superuser, CREATE revoked from
// PUBLIC and every customer role); an object whose name is taken there goes to
// a schema of its own that did not exist.
const platformSchemaQuarantineTemplate = `
SET LOCAL statement_timeout = '60s';
{{STAGE_QUARANTINE}}
{{REVOKE_HELPER}}
DO $excalibase_schema_quarantine$
DECLARE
  entry record;
  item record;
  quarantine text;
  target text;
  schema_oid oid;
BEGIN
  FOR entry IN SELECT * FROM (VALUES {{SERVICE_ROLES}}) AS v(schema_name, service_role, known_tables) LOOP
    schema_oid := to_regnamespace(entry.schema_name);
    IF schema_oid IS NULL OR to_regrole(entry.service_role) IS NULL THEN
      CONTINUE;
    END IF;
    quarantine := 'excalibase_quarantine_' || entry.schema_name;
    IF NOT EXISTS (SELECT 1 FROM {{MOVABLE}} o JOIN pg_roles r ON r.oid = o.owner WHERE {{CUSTOMER_R}}) THEN
      CONTINUE;
    END IF;

    EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I', quarantine);
    EXECUTE format('ALTER SCHEMA %I OWNER TO CURRENT_USER', quarantine);
    EXECUTE format('REVOKE CREATE ON SCHEMA %I FROM PUBLIC CASCADE', quarantine);
    PERFORM pg_temp.excalibase_revoke('CREATE', 'SCHEMA', quote_ident(quarantine),
      (SELECT coalesce(array_agg(DISTINCT r.rolname), ARRAY[]::text[]) FROM pg_namespace n, aclexplode(n.nspacl) a
       JOIN pg_roles r ON r.oid = a.grantee WHERE n.nspname = quarantine AND a.privilege_type = 'CREATE' AND {{CUSTOMER_R}}));

    FOR item IN
      SELECT o.kind, o.ident, o.object_oid, o.collides, r.rolname AS owner_name
      FROM {{MOVABLE_IN_QUARANTINE}} o JOIN pg_roles r ON r.oid = o.owner WHERE {{CUSTOMER_R}}
    LOOP
      target := quarantine;
      IF item.collides THEN
        target := NULL;
        FOR attempt IN 0..{{LAST_TRY}} LOOP
          target := quarantine || '_' || item.object_oid || CASE WHEN attempt = 0 THEN '' ELSE '_' || attempt END;
          EXIT WHEN to_regnamespace(target) IS NULL;
          target := NULL;
        END LOOP;
        IF target IS NULL THEN
          RAISE EXCEPTION 'every quarantine schema name for an object in schema % is taken; it is ` + platformSchemaLeftMarker + `', entry.schema_name
            USING ERRCODE = 'insufficient_privilege';
        END IF;
        EXECUTE format('CREATE SCHEMA %I', target);
      END IF;
      EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', target, item.owner_name);
      EXECUTE format('ALTER %s %s SET SCHEMA %I', item.kind, item.ident, target);
    END LOOP;
  END LOOP;
END
$excalibase_schema_quarantine$;
`

// The refusal is the last script: a relation a customer role owns that is not
// one of the platform's, or a platform table with a policy, default, CHECK or
// index calling a routine the platform does not own, is left for an operator,
// and the project is reported rather than passed as clean.
const platformSchemaRefusalTemplate = `
SET LOCAL statement_timeout = '60s';
{{STAGE_REFUSAL}}
DO $excalibase_schema_refusal$
DECLARE
  entry record;
  schema_oid oid;
BEGIN
  FOR entry IN SELECT * FROM (VALUES {{SERVICE_ROLES}}) AS v(schema_name, service_role, known_tables) LOOP
    schema_oid := to_regnamespace(entry.schema_name);
    IF schema_oid IS NULL OR to_regrole(entry.service_role) IS NULL THEN
      CONTINUE;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
               WHERE c.relnamespace = schema_oid AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S') AND {{CUSTOMER_R}})
       OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
                  WHERE c.relnamespace = schema_oid AND NOT {{CUSTOMER_R}} AND c.oid IN {{TAINTED_TABLES}}) THEN
      RAISE EXCEPTION 'schema % holds a relation or a table that is ` + platformSchemaLeftMarker + `', entry.schema_name
        USING ERRCODE = 'insufficient_privilege';
    END IF;
  END LOOP;
END
$excalibase_schema_refusal$;
`

// platformSchemaDriftProbeSQL answers t while the platform's schemas are not as
// the repair leaves them (EXC-821): the excalibase schema is tainted, or in auth
// or excalibase_cdc a customer role owns the schema or an object, holds any
// privilege, or PUBLIC can execute a function; or a trigger, rule, policy,
// default, CHECK or index there is not the platform's.
func platformSchemaDriftProbeSQL() string {
	return platformSchemaFragments().Replace(schemaScoped("to_regnamespace(v.schema_name)").Replace(platformSchemaDriftProbeTemplate))
}

const platformSchemaDriftProbeTemplate = `SELECT COALESCE(
  (to_regnamespace('excalibase') IS NOT NULL AND {{TAINTED}})
  OR EXISTS (
    SELECT 1 FROM (VALUES {{SERVICE_ROLES}}) AS v(schema_name, service_role, known_tables)
    WHERE to_regnamespace(v.schema_name) IS NOT NULL AND (
      EXISTS (SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
              WHERE n.oid = to_regnamespace(v.schema_name) AND {{CUSTOMER_R}})
      OR EXISTS (SELECT 1 FROM pg_roles r
                 WHERE {{CUSTOMER_R}} AND (has_schema_privilege(r.oid, to_regnamespace(v.schema_name), 'CREATE')
                                           OR has_schema_privilege(r.oid, to_regnamespace(v.schema_name), 'USAGE')))
      OR has_schema_privilege('public', to_regnamespace(v.schema_name), 'CREATE')
      OR EXISTS (SELECT 1 FROM pg_roles r CROSS JOIN pg_class c
                 WHERE {{CUSTOMER_R}} AND c.relnamespace = to_regnamespace(v.schema_name) AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
                   AND has_table_privilege(r.oid, c.oid, 'SELECT, INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER'))
      OR EXISTS (SELECT 1 FROM pg_proc p WHERE p.pronamespace = to_regnamespace(v.schema_name)
                 AND has_function_privilege('public', p.oid, 'EXECUTE'))
      OR EXISTS (SELECT 1 FROM {{OWNED}} o JOIN pg_roles r ON r.oid = o.owner WHERE {{CUSTOMER_R}})
      OR EXISTS (SELECT 1 FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_roles r ON r.oid = c.relowner
                 WHERE c.relnamespace = to_regnamespace(v.schema_name) AND NOT t.tgisinternal AND NOT {{CUSTOMER_R}}
                   AND NOT EXISTS (SELECT 1 FROM pg_proc p WHERE p.oid = t.tgfoid AND NOT {{FOREIGN_PROC}}))
      OR EXISTS (SELECT 1 FROM pg_rewrite w JOIN pg_class c ON c.oid = w.ev_class JOIN pg_roles r ON r.oid = c.relowner
                 WHERE c.relnamespace = to_regnamespace(v.schema_name) AND w.rulename <> '_RETURN' AND NOT {{CUSTOMER_R}})
      OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
                 WHERE c.relnamespace = to_regnamespace(v.schema_name) AND NOT {{CUSTOMER_R}} AND c.oid IN {{TAINTED_TABLES}})
      OR EXISTS (SELECT 1 FROM pg_default_acl d CROSS JOIN LATERAL aclexplode(d.defaclacl) a JOIN pg_roles r ON r.oid = a.grantee
                 JOIN pg_roles dr ON dr.oid = d.defaclrole
                 WHERE (d.defaclnamespace = to_regnamespace(v.schema_name) OR (d.defaclnamespace = 0 AND NOT {{CUSTOMER_D}}))
                   AND d.defaclobjtype IN ('r', 'S', 'f', 'T') AND {{CUSTOMER_R}})
      OR EXISTS (SELECT 1 FROM pg_type t CROSS JOIN LATERAL aclexplode(t.typacl) a JOIN pg_roles r ON r.oid = a.grantee
                 WHERE t.typnamespace = to_regnamespace(v.schema_name) AND t.typcategory <> 'A' AND {{CUSTOMER_R}})
    )
  ), false)`

// quarantineNameTries bounds the schema names tried for one object whose name
// is taken in the quarantine schema, before the repair refuses.
const quarantineNameTries = 8

// grantedOnSchema is every explicit grant on {{NS}} and what is in it: the
// schema, relations and their columns, routines, and types and domains, each as
// the object kind and name a REVOKE takes. Only these hold a grant, so the
// revokes need only name them: the cost follows the grants that exist, never
// the number of roles the owner made.
const grantedOnSchema = `
      SELECT 'SCHEMA' AS kind, quote_ident(n.nspname) AS ident, a.grantee FROM pg_namespace n, aclexplode(n.nspacl) a WHERE n.oid = {{NS}}
      UNION SELECT CASE c.relkind WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END, c.oid::regclass::text, a.grantee FROM pg_class c, aclexplode(c.relacl) a WHERE c.relnamespace = {{NS}}
      UNION SELECT 'TABLE', c.oid::regclass::text, a.grantee FROM pg_attribute att JOIN pg_class c ON c.oid = att.attrelid, aclexplode(att.attacl) a WHERE c.relnamespace = {{NS}}
      UNION SELECT 'ROUTINE', p.oid::regprocedure::text, a.grantee FROM pg_proc p, aclexplode(p.proacl) a WHERE p.pronamespace = {{NS}}
      UNION SELECT CASE t.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, t.oid::regtype::text, a.grantee FROM pg_type t, aclexplode(t.typacl) a
        WHERE t.typnamespace = {{NS}} AND t.typcategory <> 'A'`

// revokeHelperSQL is a function in the script's own temporary schema that
// revokes privilege on one object from many roles, a few hundred at a time and
// with CASCADE, so a grant chain cannot stop it.
const revokeHelperSQL = `CREATE OR REPLACE FUNCTION pg_temp.excalibase_revoke(privilege text, kind text, ident text, roles text[]) RETURNS void LANGUAGE plpgsql AS $excalibase_revoke$
DECLARE
  first_role int := 1;
BEGIN
  WHILE first_role <= cardinality(roles) LOOP
    EXECUTE format('REVOKE %s ON %s %s FROM %s CASCADE', privilege, kind, ident, (SELECT string_agg(quote_ident(role_name), ', ') FROM unnest(roles[first_role:first_role + 199]) AS role_name));
    first_role := first_role + 200;
  END LOOP;
END
$excalibase_revoke$;`

// stageNotice names the script that is running; the error of a failed run
// carries the last one, which tells the reconcile which script failed.
func stageNotice(name string) string {
	return "DO $excalibase_stage$ BEGIN RAISE NOTICE '" + repairStageMarker + name + "'; END $excalibase_stage$;"
}

// repairStageMarker starts the notice each script of a registration prints.
const repairStageMarker = "excalibase-repair-stage:"
