# PgDog per-project backend auth — verification (EXC-326)

PgDog fronts every tenant database behind one shared address. The threat under
review: a client that reaches PgDog names a route in the startup packet
(`database`, `user`) and PgDog connects to the backend with a **shared**
credential, so tenant A could open tenant B's database.

Sources read:

- Control plane: this repo, branch `exc-326-pgdog-per-project-auth`
  (`server-go/internal/service/pgdog_notifier.go`, `provisioning.go`,
  `storage/postgres/pg_pgdog.go`, `cmd/server/main.go`).
- PgDog fork: `github.com/excalibase/pgdog`, branch `feat/postgres-config`
  at `52657e7` (unmerged; `main` is pure upstream). Line numbers below refer
  to that commit.
- Charts: `excalibase-service/charts/*`.

## 1. Flows

### Provisioning -> PgDog config (after this change)

```
Provision(req)
  |- CNPG cluster up, read secret <proj>-postgres-app          postgresql.go:206
  |     -> result.Username/Password = CNPG owner ("app")       (docker mode: "postgres" superuser, docker_postgresql.go:46)
  |- newProjectRoleCredentials()                provisioning.go:561
  |- createProjectRoles(...)                                   provisioning.go:892-925
  |     CREATE ROLE auth_admin / excalibase_app / cdc_watcher
  |     vault: projects/<id>/credentials/{admin,auth_admin,excalibase_app,cdc_watcher}
  |- registerWithPgDog(projectID, ns, dbName, engineRoles)     provisioning.go:583, 858
        |- validatePgDogRoles: only excalibase_app, auth_admin  pgdog_notifier.go:25, 94
        |- pgdog_databases: (name=<proj>, primary rw host)       upsert on (name, role, shard)  pg_pgdog.go:17
        |                   (name=<proj>, replica ro host)
        |- pgdog_users:     (excalibase_app, <proj>, appPass)     upsert on (name, database)     pg_pgdog.go:44
        |                   (auth_admin,     <proj>, authPass)
        '- NATS publish "pgdog.config.reload" (payload "reload")

Deprovision -> DeregisterCluster(projectID)                    provisioning.go:654
        |- DELETE pgdog_users   WHERE database = <proj>
        '- DELETE pgdog_databases WHERE name = <proj>
```

Before this change the single registered user was `result.Username` /
`result.Password`, i.e. the CNPG owner (k8s) or the `postgres` superuser
(docker). `excalibase_app` — the role the engines fetch from the vault — was
never registered, so no engine credential could pass PgDog's auth at all.

### PgDog client authentication (fork, unchanged by this ticket)

```
client startup packet (user, database)                         client/mod.rs:143
  |- passthrough_auth()?  -> default Disabled                   client/mod.rs:147, auth.rs:14
  |- password = databases().password((user, database))         client/mod.rs:179   <- keyed on the PAIR
  |     None -> ErrorResponse::auth, connection closed          client/mod.rs:221
  '- auth_type: Scram (default) | Md5 | Plain | Trust           auth.rs:46-49, general.rs:972
        Scram: Server::new(&password) needs the plaintext        scram/server.rs:114
```

### PgDog backend authentication (fork, unchanged)

```
pool Address::from(database, user)                             address.rs:62-74
  user     = database.user  ?? user.server_user ?? user.name
  password = database.password ?? user.server_password ?? user.password()
pgdog_users loader sets only name/database/password             postgres.rs:126-142
  -> backend user/password == the per-row client credential; nothing shared
```

### NATS realtime reload (fork)

```
PGDOG_NATS_URL set -> nats_watcher::watch                      main.rs:160
  subscribe "pgdog.config.reload"                              nats_watcher.rs:26
  on message: payload logged, never parsed                     nats_watcher.rs:38
              from_postgres(PGDOG_CONFIG_DATABASE_URL)         nats_watcher.rs:43
              reload_from_existing()
  connection: ConnectOptions::new().require_tls(false), no creds   nats_watcher.rs:65-66
```

## 2. Evidence table

| # | Claim | Evidence |
|---|-------|----------|
| E1 | Routes are per project: logical name = project id, host = that project's CNPG service | `pgdog_notifier.go:57-80` |
| E2 | Users are scoped to the project's logical database | `pgdog_notifier.go:84` (`Database: projectID`) |
| E3 | Only `excalibase_app` and `auth_admin` may be registered; owner/superuser/cdc_watcher refused before any write | `pgdog_notifier.go:25-28, 94-104`; tests `pgdog_notifier_test.go` |
| E4 | Registered passwords are the same values written to the vault | `provisioning.go:832-853` (one struct feeds both `createProjectRoles` and `pgdogRoles()`); test `TestProvision_RegistersEngineRolesWithPgDog` |
| E5 | No engine roles (no vault) -> nothing registered, no fallback to owner cred | `provisioning.go:858-870`; test `TestProvision_WithoutEngineRoles_RegistersNothingWithPgDog` |
| E6 | Pre-change: owner/superuser credential was the registered user | `git show bee56a1:server-go/internal/service/provisioning.go` L580-585 (`result.Username, result.Password`); `provisioning.go:892-895` labels that credential "admin (superuser)" |
| E7 | PgDog client auth looks the password up by `(user, database)`; unknown pair = auth error | `client/mod.rs:143, 178-181, 221` |
| E8 | Default `auth_type` is SCRAM; `trust` only via `PGDOG_AUTH_TYPE`/TOML | `auth.rs:46-47`, `general.rs:972-973`; fork values file does not set it |
| E9 | Passthrough auth default Disabled; if enabled, `databases::add(user)` creates a pool from client-supplied params | `auth.rs:14`, `client/mod.rs:147, 156-170` |
| E10 | Backend credential = the row's own password (no `server_user`/`server_password`, no `database.password`) | `address.rs:62-74`, `postgres.rs:126-142` |
| E11 | `pgdog_users.password` is stored as plaintext; SCRAM server needs it | `sql/pgdog_config_tables.sql:24`, `scram/server.rs:114`, `postgres.rs:138` |
| E12 | NATS payload is never interpreted; reload always re-reads platform DB | `nats_watcher.rs:38-58` |
| E13 | NATS connection is unauthenticated and TLS not required | `nats_watcher.rs:65-66` |
| E14 | Fork DDL has no uniqueness on `(name, role, shard)`; old `ON CONFLICT DO NOTHING` could never fire | `sql/pgdog_config_tables.sql:5-18`; `git show bee56a1:server-go/internal/storage/postgres/pg_pgdog.go` L13 |
| E15 | Control plane now owns the DDL with the unique route key | `migrations/000019_pgdog_config.up.sql` |
| E16 | Notifier wires when `PLATFORM_DB_URL` and `NATS_URL` are both set; `platform-aio` sets both, so provisioning there was already writing rows (and, before migration 000019, logging `WARN: pgdog register: relation "pgdog_databases" does not exist` and continuing) | `cmd/server/main.go:574`; `excalibase-service/charts/platform-aio/templates/provisioning.yaml:55-60` |
| E17 | No chart deploys PgDog; `platform-aio` only mentions it in a runbook; fork ships values for the upstream Helm chart | `excalibase-service/charts` grep; `pgdog/charts/excalibase-pgdog-values.yaml` |
| E18 | Engines still receive the direct CNPG host from the vault, not a PgDog address | `provisioning.go:895, 916-925` (`"host": result.Host`) |

## 3. Verdicts

| Q | Question | Verdict | Why |
|---|----------|---------|-----|
| 1 | What provisioning registers | **GAP -> FIXED** | Per project: `pgdog_databases` rows named by project id with that project's CNPG hosts (E1), `pgdog_users` rows scoped to that name (E2). Nothing is shared across tenants. The gap was *which* credential: the CNPG owner / docker superuser (E6), i.e. the highest-privileged role in the tenant DB, sitting in plaintext in the platform DB. Now only `excalibase_app` and `auth_admin` with their vault passwords are registered (E3, E4), and the owner credential is refused by construction (E3). |
| 2 | Client auth / backend auth | **SAFE** (with two operator invariants) | Client: SCRAM by default against the per-row password (E7, E8). Backend: same per-row credential, no shared superuser anywhere in the fork's loader (E10). Invariants the fork must keep: `auth_type != trust` and `passthrough_auth = disabled` when Postgres-backed config is active (E8, E9) — under `trust`, knowing the role name `excalibase_app` would be enough to name any project; under passthrough a client could mint a pool for an unregistered route. The fork patch turns both into a startup refusal. |
| 3 | Can a client for A open B; is the vault password the PgDog password | **SAFE / YES** | Lookup is on the `(user, database)` pair (E7): `(excalibase_app, proj-A)` and `(excalibase_app, proj-B)` are distinct rows with distinct 32-char generated passwords, so A's password cannot authenticate against B's row. The password PgDog checks and forwards is byte-identical to `projects/<id>/credentials/<role>.password` (E4). Residual: PgDog needs the plaintext (E11), so the platform DB now holds engine-role passwords in clear — weaker than the vault's AES-GCM at rest. Tracked below. |
| 4 | Can an unauthenticated NATS publisher inject routes | **SAFE for injection, GAP for availability** | The payload is discarded and routes come only from the platform DB (E12), so a publisher cannot add or alter a route. It *can* force unbounded pool rebuilds (E13). Ties to EXC-324: the watcher should authenticate to NATS and coalesce bursts (fork patch). |

Overall: the "shared credential + client-chosen route" threat is **not present**
in the design — routing was already keyed on `(user, database)` with per-row
credentials. The real defects were on the provisioning side (wrong, over-
privileged credential; no uniqueness on the route key) and are fixed here.

## 4. Fix list

Done in this branch (control plane):

1. `pgdog_notifier.go` — `RegisterCluster(ctx, projectID, namespace, dbName, roles []PgDogRole)`
   with an allowlist (`excalibase_app`, `auth_admin`); `ErrPgDogRoleNotRoutable`
   is returned before any write. `DeregisterCluster(ctx, projectID)` removes
   every user of the project, not one username.
2. `provisioning.go` — engine role passwords are generated once
   (`newProjectRoleCredentials`) and shared between the vault write and the
   PgDog registration; no roles -> no registration.
3. `storage/store.go`, `pg_pgdog.go` — `RemovePgDogUsers(ctx, database)`;
   `RegisterPgDogDatabase` upserts on `(name, role, shard)` and refreshes
   host/port/flags; `RegisterPgDogUser` also refreshes `pool_size`/`active`.
4. `migrations/000019_pgdog_config.{up,down}.sql` — control plane owns the
   `pgdog_databases` / `pgdog_users` DDL (column set identical to the fork's
   so its `CREATE TABLE IF NOT EXISTS` is a no-op), dedups any pre-existing
   duplicate routes and adds `uq_pgdog_databases_route`.
5. Tests: `pgdog_notifier_test.go` (allowlist, empty/mixed sets, deregister
   by project, full Provision/Deprovision wiring), updated
   `coverage_gap_test.go` fakes, `storage/postgres/coverage_gap_test.go`
   (upsert keeps one row, remove-by-database leaves other projects), and
   the NATS/Postgres integration test now runs on the real migration.

Belongs in the PgDog fork — `docs/pgdog-fork-patch-exc-326.diff` (not applied):

6. `sql/pgdog_config_tables.sql` — `UNIQUE(name, role, shard)` to match
   migration 000019.
7. `pgdog/src/main.rs` — `refuse_unauthenticated_routing()`: exit at start
   when `auth_type = trust` or passthrough auth is on while
   `PGDOG_CONFIG_DATABASE_URL` is set.
8. `pgdog/src/config/nats_watcher.rs` — authenticate the NATS connection
   (`PGDOG_NATS_USER`/`PGDOG_NATS_PASSWORD` or `PGDOG_NATS_CREDS_FILE`,
   optional `PGDOG_NATS_TLS=true`), warn when connecting anonymously, and
   coalesce reload bursts into one reload per 500 ms window.
9. `pgdog/src/backend/databases.rs` — `reload()` (SIGHUP path) wraps
   `block_on` in `tokio::task::block_in_place`; as written it panics on a
   runtime worker thread.

Deployment / follow-ups (not code in this repo):

10. PgDog is not deployed by any chart today (E17). Deploying it means:
    install the upstream chart with the fork image and
    `charts/excalibase-pgdog-values.yaml`, set `PGDOG_CONFIG_DATABASE_URL`
    to a platform-DB role that can `SELECT` only `pgdog_databases` /
    `pgdog_users`, and set the NATS credentials from item 8.
11. Engines still get the direct CNPG host from the vault (E18). Routing
    them through PgDog requires writing the PgDog service address as `host`
    and the project id as `database` in the `excalibase_app` / `auth_admin`
    vault entries — a deliberate switch, tracked separately.
12. Plaintext engine passwords in `pgdog_users` (E11): either let PgDog read
    credentials from the vault service instead of the table, or store an
    encrypted value and give PgDog the key. Until then, the platform DB role
    used by PgDog must be read-only and the table must not be exposed via
    any API.
