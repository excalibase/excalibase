package pgroles

import "strings"

// RuntimeRole is the login a customer's apps and functions connect with
// (EXC-765). The owner's login stays the platform's: on Postgres 16 and later
// it holds CREATEROLE, so code that held it could mint or change logins.
const RuntimeRole = "excalibase_runtime"

// RuntimeRoleSQL creates the runtime login if absent and gives it the owner's
// and the platform role's rights over objects, not their attributes: it
// inherits their table, sequence and schema privileges and the ownership
// checks DDL makes, and has no CREATEROLE, no superuser and no replication.
// From Postgres 16 the memberships carry no SET, so it cannot become either
// role; before it the role functions refuse any session that is not the owner's.
// Tables it creates are shared with the owner, the platform role and the
// reader through default privileges.
//
// verifier is the SCRAM verifier of the login's password, never the password.
func RuntimeRoleSQL(owner, verifier string) string {
	return strings.NewReplacer(
		"{{RUNTIME}}", quoteLiteral(RuntimeRole),
		"{{VERIFIER}}", textLiteral(verifier),
		"{{OWNER}}", textLiteral(owner),
		"{{APP}}", quoteLiteral(appRole),
		"{{READER}}", quoteLiteral(ReaderRole),
	).Replace(runtimeRoleTemplate)
}

// RuntimeLockSQL leaves a runtime login the platform did not file unable to
// sign in: a copy restored from a project that had one still carries its password.
func RuntimeLockSQL() string {
	return strings.ReplaceAll(`
DO $excalibase_runtime_lock$
BEGIN
  IF to_regrole({{RUNTIME}}) IS NOT NULL THEN
    EXECUTE format('ALTER ROLE %I NOLOGIN', {{RUNTIME}});
  END IF;
END
$excalibase_runtime_lock$;
`, "{{RUNTIME}}", quoteLiteral(RuntimeRole))
}

const runtimeRoleTemplate = `
DO $excalibase_runtime_role$
DECLARE
  runtime_role CONSTANT text := {{RUNTIME}};
  owner_role CONSTANT text := {{OWNER}};
  platform_role CONSTANT text := {{APP}};
  reader_role CONSTANT text := {{READER}};
  parent text;
BEGIN
  IF to_regrole(runtime_role) IS NULL THEN
    EXECUTE format('CREATE ROLE %I WITH LOGIN PASSWORD %L', runtime_role, {{VERIFIER}});
  END IF;
  EXECUTE format('ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS', runtime_role);
  -- Granted again each time so a membership an older run made with SET is replaced.
  FOREACH parent IN ARRAY ARRAY[owner_role, platform_role] LOOP
    EXECUTE format('REVOKE %I FROM %I', parent, runtime_role);
    IF current_setting('server_version_num')::int >= 160000 THEN
      EXECUTE format('GRANT %I TO %I WITH INHERIT TRUE, SET FALSE', parent, runtime_role);
    ELSE
      EXECUTE format('GRANT %I TO %I', parent, runtime_role);
    END IF;
  END LOOP;
  -- The owner's session runs what a table's triggers and defaults call, so it
  -- only reads what the app made; excalibase_app, which cannot manage roles, writes it.
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I GRANT SELECT ON TABLES TO %I', runtime_role, owner_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON TABLES TO %I', runtime_role, platform_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I GRANT ALL ON SEQUENCES TO %I', runtime_role, platform_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON TABLES FROM %I', runtime_role, owner_role);
  IF to_regrole(reader_role) IS NOT NULL THEN
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I GRANT SELECT ON TABLES TO %I', runtime_role, reader_role);
    EXECUTE format('ALTER DEFAULT PRIVILEGES FOR ROLE %I GRANT SELECT ON SEQUENCES TO %I', runtime_role, reader_role);
  END IF;
-- Raised again without its context, which quotes the statement and so the verifier.
EXCEPTION WHEN OTHERS OR query_canceled THEN
  RAISE EXCEPTION USING ERRCODE = SQLSTATE, MESSAGE = SQLERRM;
END
$excalibase_runtime_role$;
`
