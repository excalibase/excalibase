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
		"{{LAST_TRY}}", strconv.Itoa(quarantineNameTries-1), "{{MOVABLE}}", scoped.Replace(movableObjects), "{{MOVABLE_IN_QUARANTINE}}", scoped.Replace(movableWithTargets))
}

// The lockdown takes the schema back, reclaims the platform's own tables a
// customer role owns (the known set, never moved), drops triggers and rules the
// platform did not put on its tables, and revokes every grant a customer role
// holds and PUBLIC's EXECUTE. Nothing in it can refuse or collide.
const platformSchemaLockdownTemplate = `
SET LOCAL statement_timeout = '60s';
DO $excalibase_schema_lockdown$
DECLARE
  entry record;
  item record;
  holder record;
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

    FOR holder IN SELECT r.rolname FROM pg_roles r WHERE {{CUSTOMER_R}} LOOP
      EXECUTE format('REVOKE ALL ON SCHEMA %I FROM %I', entry.schema_name, holder.rolname);
      EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %I FROM %I', entry.schema_name, holder.rolname);
      EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA %I FROM %I', entry.schema_name, holder.rolname);
      EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %I FROM %I', entry.schema_name, holder.rolname);
    END LOOP;
    EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', entry.schema_name);
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA %I FROM PUBLIC', entry.schema_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA %I FROM PUBLIC', entry.schema_name);
    EXECUTE format('REVOKE ALL ON ALL FUNCTIONS IN SCHEMA %I FROM PUBLIC', entry.schema_name);
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
DO $excalibase_schema_quarantine$
DECLARE
  entry record;
  item record;
  holder record;
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
    EXECUTE format('REVOKE CREATE ON SCHEMA %I FROM PUBLIC', quarantine);
    FOR holder IN SELECT r.rolname FROM pg_roles r WHERE {{CUSTOMER_R}} LOOP
      EXECUTE format('REVOKE CREATE ON SCHEMA %I FROM %I', quarantine, holder.rolname);
    END LOOP;

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
    )
  ), false)`

// quarantineNameTries bounds the schema names tried for one object whose name
// is taken in the quarantine schema, before the repair refuses.
const quarantineNameTries = 8
