#!/bin/sh
# Roles for the data database (runs once, on a fresh volume). app_user's password
# comes from APP_DB_PASSWORD; auth_admin gets a random one, since nothing in this
# stack logs in as it here.
set -eu

auth_admin_password=$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -v app_password="$APP_DB_PASSWORD" -v auth_admin_password="$auth_admin_password" <<'SQL'
-- Auth admin role (auth service creates its own schema via migrations)
SELECT format('CREATE ROLE auth_admin WITH LOGIN PASSWORD %L', :'auth_admin_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'auth_admin') \gexec
GRANT CREATE ON DATABASE hana TO auth_admin;
GRANT CREATE, USAGE ON SCHEMA public TO auth_admin;

-- App user for RLS (non-superuser so RLS policies are enforced)
SELECT format('CREATE ROLE app_user WITH LOGIN PASSWORD %L', :'app_password')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'app_user') \gexec
GRANT USAGE ON SCHEMA public TO app_user;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO app_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO app_user;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO app_user;
SQL
