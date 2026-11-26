package service

import "strings"

// platformSchemaRepairSQL puts the platform's schemas back in the platform's
// hands and is safe to run on a clean project (EXC-821). It never adopts what a
// customer role made: such an object (relation, routine, type, domain or
// operator) moves out of auth or excalibase_cdc into excalibase_quarantine_<schema>,
// its customer still the owner, so it gains nothing and loses nothing. Triggers
// and rules on the platform's own tables that the platform did not put there are
// dropped first, since they would run as the table's owner. A platform table with
// a policy, default, CHECK or index calling a routine the platform does not own
// is not touched: the repair raises, and the project is left for an operator.
// Every grant a customer role holds on these schemas is revoked, and PUBLIC loses
// EXECUTE on their functions. The excalibase schema is the rebuild's.
func platformSchemaRepairSQL() string {
	return platformSchemaFragments().Replace(schemaScoped("schema_oid").Replace(platformSchemaRepairTemplate))
}

// schemaScoped fills the fragments that depend on which schema is read.
func schemaScoped(namespace string) *strings.Replacer {
	owned := strings.NewReplacer("{{NS}}", namespace).Replace(customerOwnedObjects)
	return strings.NewReplacer("{{OWNED}}", owned, "{{TAINTED_TABLES}}", taintedTables)
}

const platformSchemaRepairTemplate = `
DO $excalibase_schema_repair$
DECLARE
  entry record;
  item record;
  holder record;
  quarantine text;
  schema_oid oid;
BEGIN
  FOR entry IN SELECT * FROM (VALUES {{SERVICE_ROLES}}) AS v(schema_name, service_role) LOOP
    schema_oid := to_regnamespace(entry.schema_name);
    IF schema_oid IS NULL OR to_regrole(entry.service_role) IS NULL THEN
      CONTINUE;
    END IF;
    quarantine := 'excalibase_quarantine_' || entry.schema_name;

    IF EXISTS (SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
               WHERE n.oid = schema_oid AND {{CUSTOMER_R}}) THEN
      EXECUTE format('ALTER SCHEMA %I OWNER TO CURRENT_USER', entry.schema_name);
    END IF;

    -- Triggers first: a routine moved out from under one would keep it running.
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

    IF EXISTS (SELECT 1 FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
               WHERE c.relnamespace = schema_oid AND NOT {{CUSTOMER_R}} AND c.oid IN {{TAINTED_TABLES}}) THEN
      RAISE EXCEPTION 'a table in schema % has a policy, default, check or index calling a routine the platform does not own; it is left for an operator', entry.schema_name
        USING ERRCODE = 'insufficient_privilege';
    END IF;

    IF EXISTS (SELECT 1 FROM {{OWNED}} o JOIN pg_roles r ON r.oid = o.owner WHERE {{CUSTOMER_R}}) THEN
      EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I', quarantine);
      EXECUTE format('REVOKE ALL ON SCHEMA %I FROM PUBLIC', quarantine);
      FOR item IN
        SELECT c.oid::regclass::text AS name, c.relkind, r.rolname AS owner_name FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
        WHERE c.relnamespace = schema_oid AND c.relkind IN ('r', 'p', 'f', 'v', 'm', 'c') AND {{CUSTOMER_R}}
      LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', quarantine, item.owner_name);
        EXECUTE format('ALTER %s %s SET SCHEMA %I',
          CASE item.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW' WHEN 'c' THEN 'TYPE' ELSE 'TABLE' END, item.name, quarantine);
      END LOOP;
      FOR item IN
        SELECT c.oid::regclass::text AS name, r.rolname AS owner_name FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
        WHERE c.relnamespace = schema_oid AND c.relkind = 'S' AND {{CUSTOMER_R}}
      LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', quarantine, item.owner_name);
        EXECUTE format('ALTER SEQUENCE %s SET SCHEMA %I', item.name, quarantine);
      END LOOP;
      FOR item IN
        SELECT p.oid::regprocedure::text AS name, r.rolname AS owner_name FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner
        WHERE p.pronamespace = schema_oid AND {{CUSTOMER_R}}
      LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', quarantine, item.owner_name);
        EXECUTE format('ALTER ROUTINE %s SET SCHEMA %I', item.name, quarantine);
      END LOOP;
      FOR item IN
        SELECT t.oid::regtype::text AS name, t.typtype, r.rolname AS owner_name FROM pg_type t JOIN pg_roles r ON r.oid = t.typowner
        WHERE t.typnamespace = schema_oid AND t.typrelid = 0 AND t.typcategory <> 'A' AND {{CUSTOMER_R}}
      LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', quarantine, item.owner_name);
        EXECUTE format('ALTER %s %s SET SCHEMA %I', CASE item.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, item.name, quarantine);
      END LOOP;
      FOR item IN
        SELECT op.oprname, op.oprleft, op.oprright, r.rolname AS owner_name FROM pg_operator op JOIN pg_roles r ON r.oid = op.oprowner
        WHERE op.oprnamespace = schema_oid AND {{CUSTOMER_R}}
      LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I', quarantine, item.owner_name);
        EXECUTE format('ALTER OPERATOR %I.%s (%s, %s) SET SCHEMA %I', entry.schema_name, item.oprname,
          coalesce(item.oprleft::regtype::text, 'NONE'), coalesce(item.oprright::regtype::text, 'NONE'), quarantine);
      END LOOP;
    END IF;

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
$excalibase_schema_repair$;
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
    SELECT 1 FROM (VALUES {{SERVICE_ROLES}}) AS v(schema_name, service_role)
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
