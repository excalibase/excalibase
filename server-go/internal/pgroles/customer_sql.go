package pgroles

import (
	"encoding/hex"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// Postgres 15 and earlier have only these: CREATEROLE there lets a role grant
// itself pg_execute_server_program.
const (
	CreateRoleSignature        = "excalibase.create_role(text, text, boolean, text[])"
	AlterRolePasswordSignature = "excalibase.alter_role_password(text, text)"
	DropRoleSignature          = "excalibase.drop_role(text)"
)

// PinnedSearchPath starts every script the superuser runs in a tenant
// database: an unqualified call must never find a function a customer put in
// public.
const PinnedSearchPath = "SET search_path = pg_catalog, pg_temp;\n"

// SquatGuardSQL refuses to go on when a platform role exists but a customer
// administers it: on Postgres 16+ the owner can create any free name, and the
// platform's create-if-absent steps would otherwise adopt that role.
func SquatGuardSQL() string {
	return `
DO $excalibase_squat$
DECLARE
  squatted text;
BEGIN
  SELECT string_agg(r.rolname, ', ') INTO squatted
  FROM pg_auth_members m
  JOIN pg_roles r ON r.oid = m.roleid
  JOIN pg_roles u ON u.oid = m.member
  WHERE r.rolname = ANY (` + textArray(reservedNames) + `) AND m.admin_option AND NOT u.rolsuper;
  IF squatted IS NOT NULL THEN
    RAISE EXCEPTION 'platform role(s) % were created outside the platform', squatted USING ERRCODE = 'insufficient_privilege';
  END IF;
END
$excalibase_squat$;
`
}

// appRole is the platform role Studio runs as; it calls the functions for the owner.
const appRole = "excalibase_app"

// CustomerRoleSQL gives the owner CREATEROLE from Postgres 16 and installs the
// functions. A role is the owner's when the owner holds ADMIN OPTION on it, as
// Postgres 16 grants a CREATEROLE role for each role it creates. The bodies
// call only pg_catalog: excalibase_app can create objects in their schema,
// which is also why an existing function is dropped rather than replaced.
func CustomerRoleSQL(owner string) string {
	replacer := strings.NewReplacer(
		"{{OWNER}}", textLiteral(owner),
		"{{APP_ROLE}}", quoteLiteral(appRole),
		"{{DECLARE}}", declarations(owner),
		"{{NAME_GUARD}}", nameGuard,
		"{{CUSTOMER_GUARD_TARGET}}", customerGuard("target"),
		"{{CUSTOMER_GUARD_PARENT}}", customerGuard("parent"),
		"{{CREATE}}", CreateRoleSignature,
		"{{ALTER}}", AlterRolePasswordSignature,
		"{{DROP}}", DropRoleSignature,
	)
	return replacer.Replace(customerRoleTemplate)
}

// declarations are the constants every function body checks against.
func declarations(owner string) string {
	return "owner_role CONSTANT text := " + textLiteral(owner) + ";\n" +
		"  reserved_names CONSTANT text[] := " + textArray(reservedNames) + ";\n" +
		"  reserved_prefixes CONSTANT text[] := " + textArray(reservedPrefixes) + ";\n" +
		"  mongo_group CONSTANT text := " + quoteLiteral(config.DocumentDBMongoUsersGroup) + ";"
}

// nameGuard refuses a name the platform holds or may one day hold.
const nameGuard = `IF target IS NULL OR target = '' OR octet_length(target) > 63 THEN
    RAISE EXCEPTION 'a role name is 1 to 63 bytes long' USING ERRCODE = 'invalid_name';
  END IF;
  IF lower(target) = ANY (reserved_names) OR lower(target) = lower(owner_role)
     OR EXISTS (SELECT 1 FROM unnest(reserved_prefixes) AS p(prefix) WHERE starts_with(lower(target), p.prefix)) THEN
    RAISE EXCEPTION 'role name "%" is reserved for the platform', target USING ERRCODE = 'reserved_name';
  END IF;`

// customerGuard refuses any role the owner did not create, and any role
// inside a platform or predefined role, the Mongo users group included.
func customerGuard(variable string) string {
	return strings.ReplaceAll(`IF variable IS NULL OR lower(variable) = ANY (reserved_names) OR lower(variable) = lower(owner_role)
     OR EXISTS (SELECT 1 FROM unnest(reserved_prefixes) AS p(prefix) WHERE starts_with(lower(variable), p.prefix))
     OR NOT EXISTS (
       SELECT 1 FROM pg_auth_members m
       JOIN pg_roles r ON r.oid = m.roleid
       JOIN pg_roles o ON o.oid = m.member
       WHERE r.rolname = variable AND o.rolname = owner_role AND m.admin_option
         AND NOT r.rolsuper AND NOT r.rolreplication AND NOT r.rolbypassrls)
     OR EXISTS (
       SELECT 1 FROM pg_roles r JOIN pg_roles g ON g.oid <> r.oid
         AND (g.rolname = ANY (reserved_names) OR g.rolname = mongo_group OR starts_with(g.rolname, 'pg_'))
       WHERE r.rolname = variable AND pg_has_role(r.oid, g.oid, 'MEMBER')) THEN
    RAISE EXCEPTION 'role "%" is not one this project created', variable USING ERRCODE = 'insufficient_privilege';
  END IF;`, "variable", variable)
}

const customerRoleTemplate = `
DO $excalibase_owner$
DECLARE
  owner_role CONSTANT text := {{OWNER}};
BEGIN
  IF current_setting('server_version_num')::int >= 160000 THEN EXECUTE format('ALTER ROLE %I CREATEROLE', owner_role); END IF;
  EXECUTE format('GRANT USAGE ON SCHEMA excalibase TO %I', owner_role);
END
$excalibase_owner$;

DROP FUNCTION IF EXISTS {{CREATE}};
CREATE FUNCTION excalibase.create_role(role_name text, role_password text DEFAULT NULL,
  can_login boolean DEFAULT true, member_of text[] DEFAULT '{}')
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  {{DECLARE}}
  target text := role_name;
  parent text;
BEGIN
  {{NAME_GUARD}}
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = target) THEN
    RAISE EXCEPTION 'role "%" already exists', target USING ERRCODE = 'duplicate_object';
  END IF;
  FOREACH parent IN ARRAY coalesce(member_of, '{}') LOOP
    {{CUSTOMER_GUARD_PARENT}}
  END LOOP;
  BEGIN
    EXECUTE format('CREATE ROLE %I WITH %s NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD %L',
      target, CASE WHEN can_login THEN 'LOGIN' ELSE 'NOLOGIN' END, role_password);
    IF current_setting('server_version_num')::int >= 160000 THEN
      EXECUTE format('GRANT %I TO %I WITH ADMIN TRUE, INHERIT FALSE, SET FALSE', target, owner_role);
    ELSE
      EXECUTE format('GRANT %I TO %I WITH ADMIN OPTION', target, owner_role);
    END IF;
    FOREACH parent IN ARRAY coalesce(member_of, '{}') LOOP
      -- Granted in the owner's name on 16+, so the owner can revoke it.
      IF current_setting('server_version_num')::int >= 160000 THEN
        EXECUTE format('GRANT %I TO %I GRANTED BY %I', parent, target, owner_role);
      ELSE
        EXECUTE format('GRANT %I TO %I', parent, target);
      END IF;
    END LOOP;
  EXCEPTION WHEN OTHERS THEN
    -- Raised again without its context, which quotes the statement and so the password.
    RAISE EXCEPTION USING ERRCODE = SQLSTATE, MESSAGE = SQLERRM;
  END;
END
$fn$;
ALTER FUNCTION {{CREATE}} OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION {{CREATE}} FROM PUBLIC;

DROP FUNCTION IF EXISTS {{ALTER}};
CREATE FUNCTION excalibase.alter_role_password(role_name text, role_password text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  {{DECLARE}}
  target text := role_name;
BEGIN
  {{CUSTOMER_GUARD_TARGET}}
  BEGIN
    EXECUTE format('ALTER ROLE %I PASSWORD %L', target, role_password);
  EXCEPTION WHEN OTHERS THEN
    RAISE EXCEPTION USING ERRCODE = SQLSTATE, MESSAGE = SQLERRM;
  END;
END
$fn$;
ALTER FUNCTION {{ALTER}} OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION {{ALTER}} FROM PUBLIC;

DROP FUNCTION IF EXISTS {{DROP}};
CREATE FUNCTION excalibase.drop_role(role_name text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $fn$
DECLARE
  {{DECLARE}}
  target text := role_name;
BEGIN
  {{CUSTOMER_GUARD_TARGET}}
  EXECUTE format('DROP ROLE %I', target);
END
$fn$;
ALTER FUNCTION {{DROP}} OWNER TO CURRENT_USER;
REVOKE ALL ON FUNCTION {{DROP}} FROM PUBLIC;

DO $excalibase_grants$
DECLARE
  owner_role CONSTANT text := {{OWNER}};
BEGIN
  EXECUTE format('GRANT EXECUTE ON FUNCTION {{CREATE}} TO %I, %I', owner_role, {{APP_ROLE}});
  EXECUTE format('GRANT EXECUTE ON FUNCTION {{ALTER}} TO %I, %I', owner_role, {{APP_ROLE}});
  EXECUTE format('GRANT EXECUTE ON FUNCTION {{DROP}} TO %I, %I', owner_role, {{APP_ROLE}});
END
$excalibase_grants$;
`

// textLiteral renders s as an expression Postgres decodes itself; only hex
// digits reach the statement, so no quoting context can be broken out of.
func textLiteral(s string) string {
	return "convert_from(decode('" + hex.EncodeToString([]byte(s)) + "', 'hex'), 'UTF8')"
}

// quoteLiteral quotes one of this package's own constants, which are plain
// lowercase identifiers (TestReservedNamesArePlainIdentifiers).
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func textArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, quoteLiteral(value))
	}
	return "ARRAY[" + strings.Join(quoted, ", ") + "]::text[]"
}
