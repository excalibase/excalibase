# Excalibase

An open-source platform as a service. A project gets a database (Postgres, MySQL or DocumentDB), a GraphQL and REST API generated from its schema, row and column permissions per role, auth for your app's end users, TypeScript functions, hosting for your containers next to the database, and static Sites with your own domain. Run it on the hosted beta at [excalibase.io](https://excalibase.io), or self-host the whole platform on one box or on Kubernetes.

This repository is the control plane and Studio, the web console. The API engine is [excalibase-graphql](https://github.com/excalibase/excalibase-graphql), end-user auth is [excalibase-auth](https://github.com/excalibase/excalibase-auth), change capture is [excalibase-watcher](https://github.com/excalibase/excalibase-watcher), the client is [@excalibase/sdk](https://github.com/excalibase/excalibase-sdk-js). All Apache-2.0.

![Studio project overview](https://excalibase.io/screens/studio-overview.webp)

## Try it on one machine

Linux with Docker Engine 24+ and the Compose plugin, ports 80 and 443 free:

```bash
git clone --depth 1 --branch v1.5.1 https://github.com/excalibase/excalibase.git
cd excalibase/deploy/single-host
./init.sh --domain localhost --admin-email you@example.com
docker compose up -d
docker compose logs -f bootstrap   # prints the first admin's password once
```

Open https://studio.localhost and sign in as `admin`. Rootless Podman, a real domain, container apps and what a single host does not do (high availability, edge functions, realtime) are in the [self-hosting guide](https://excalibase.io/docs/self-hosting). Kubernetes installs use the Helm charts in [`charts/`](charts).

## What the control plane does

- **Projects** — a project is a container of services: 0 or 1 database, apps, functions, storage and Sites, with organization and project roles in Studio
- **Two provisioners** — Kubernetes (CloudNativePG for Postgres, MOCO for MySQL) or a single Docker / rootless Podman host behind an engine-socket proxy
- **Plans** — FREE (1 instance), STANDARD (3 instances, synchronous replication), ENTERPRISE (5 instances)
- **Backups and PITR** — scheduled base backups, WAL archiving, restore to a point in time or into a new project
- **Schema management** — tables, columns, roles, extensions, policies, functions, triggers and indexes from Studio or the API
- **Permissions** — per role, table and operation, served to the API engine, which enforces them itself
- **Realtime** — per-table change capture toggled from Studio, backed by the watcher
- **Edge functions** — per-project Deno workers with secrets, an outbound allowlist and log streaming
- **Containers** — any image next to the database, under gVisor, with custom domains and roll back
- **Sites** — static frontends served from Cloudflare's edge, with custom domains
- **MCP** — a coding agent can create tables, set permissions and deploy, limited to its token's scopes
- **Vault** — Shamir secret sharing for credentials, AES-256-GCM at rest

## Using the engine with your own database

The platform hosts the databases and the apps it provisions, so a database
you run yourself is not a project here. To put the API engine in front of
a database you already operate, run the open-source engine standalone —
see [github.com/excalibase/excalibase-graphql](https://github.com/excalibase/excalibase-graphql).

## Architecture

```
server-go/       Go backend (chi router, client-go, Docker SDK)
├── cmd/server/    Entry point, router wiring, mode selection
├── internal/
│   ├── handler/     HTTP handlers (auth, orgs, provisioning, schema,
│   │                edge functions, realtime, setup, vaultapi, etc.)
│   ├── service/     Business logic (provisioning, metrics, backup, migrations,
│   │                snapshots, alerting)
│   ├── provisioner/ Strategy pattern (PostgreSQL/CNPG, Docker)
│   ├── k8s/         client-go wrapper, Helm SDK, CRD builders, mock
│   ├── edgefn/      Edge function store + Deno runtime client + esbuild bundler
│   ├── schema/      SQL introspection + parameterized query builder
│   ├── storage/
│   │   ├── sqlite/    SQLite store (self-hosted)
│   │   └── postgres/  Postgres store (cloud, via CNPG)
│   ├── vault/        Shamir-based in-process vault (Postgres + AES-256-GCM)
│   ├── vaultclient/  VaultClient interface — in-process or HTTP-remote
│   ├── auth/         3-level RBAC (platform/org/project), JWT, argon2id
│   ├── middleware/   CORS, security headers, TenantContext
│   └── security/     AES encryption for filesystem state
frontend/        React 18 studio (Vite, Tailwind, TanStack)
charts/
├── platform-base/    CNPG platform-db cluster (3 instances, S3 backup)
├── platform-aio/     All-in-one bundle (CNPG + provisioning + deps)
└── provisioning/     Provisioning server Helm chart
deno-server/     Deno edge functions runtime (Web Workers, sandboxed)
```

The Go server uses `client-go` natively — no kubectl shell-outs. CRDs are built as `unstructured.Unstructured` objects and applied via the dynamic client.

## Developing from source

### Prerequisites

- For K8s mode: any cluster — managed (EKS/GKE/AKS), self-hosted (RKE2), or single-node (k0s/k3s/MicroK8s, rootless OK). For Docker mode: a Docker daemon (local or remote over TLS).
- Go 1.26.10+
- Node.js 18+ (for frontend)

### 1. Install operators

```bash
# CloudNativePG
kubectl apply --server-side -f \
  https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.30/releases/cnpg-1.30.1.yaml

# Metrics Server
kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
kubectl patch deployment metrics-server -n kube-system \
  --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
```

### 2. Start the Go server

```bash
cd server-go
go build -o excalibase-server ./cmd/server/
PORT=24005 PUBLIC_PORT=24006 TRUSTED_PROXY_CIDRS=127.0.0.1 STORAGE_PATH=../provisioning-data ./excalibase-server
```

### 3. Start the frontend

```bash
cd frontend
npm install
npm run dev
# → http://localhost:5173
```

### 4. Initialize vault and authenticate

The studio's setup wizard at `http://localhost:5173/setup` walks through vault init + first-admin creation in one flow. Or do it manually:

```bash
# Initialize vault (returns unseal shares); auto-unseals when threshold = shares
curl -s -X POST http://localhost:24005/api/vault/init \
  -H "Content-Type: application/json" \
  -d '{"shares": 5, "threshold": 3}'

# Unseal (repeat with 3 different shares)
curl -s -X POST http://localhost:24005/api/vault/unseal \
  -H "Content-Type: application/json" \
  -d '{"share": "<share-hex>"}'

# Register the first admin with the one-time setup token, burned on
# use: SETUP_TOKEN, which Kubernetes requires (provisioning refuses to start
# without it until an admin exists); only the docker provisioner started
# without SETUP_TOKEN prints a generated one to its log ("First-admin setup token: ...")
curl -s -X POST http://localhost:24005/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "<password>", "email": "admin@example.com", "setupToken": "<token from the server log>"}'

# Login (returns bearer token)
curl -s -X POST http://localhost:24005/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "<password>"}'
# → {"token": "excali_..."}
```

### 5. Provision a database

```bash
TOKEN="excali_..."
curl -X POST http://localhost:24005/api/provision \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "projectName": "my-db",
    "orgId": "myorg",
    "databaseType": "POSTGRESQL",
    "backup": {"enabled": true, "schedule": "0 2 * * *", "retention": 30},
    "parameters": {"work_mem": "16MB"}
  }'
```

`parameters` accepts only tenant-tunable settings, each bounded by the tier: `work_mem`, `maintenance_work_mem`, `effective_cache_size`, `random_page_cost`, `seq_page_cost`, `effective_io_concurrency`, `default_statistics_target`, `jit`, `default_transaction_isolation`, `lock_timeout`, `deadlock_timeout`. Anything else (timeouts, `max_connections`, `shared_preload_libraries`, logging, TLS) is set by the platform and refused with 400.

## API Reference

All endpoints (except `GET /healthz`, `GET /api/config`, login/register, vault init/unseal/status, and the public function invoke) require authentication via `Authorization: Bearer <token>` header.

### Authorization

Every route with a project in its path binds that project to the caller before the handler runs: the caller must be a member of the project's org (some routes require a minimum org role — Owner ⊇ Admin ⊇ Developer ⊇ Viewer), a PAT created with `projectId` never leaves that project, and a `read`-scoped PAT cannot mutate. A project the caller may not see answers **404** (not 403); a role or scope shortfall answers **403**. The full route → role table is in [OPERATOR.md §6.2](OPERATOR.md#62-project-route-authorization-path--caller-binding).

### Public

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/healthz` | Liveness probe |
| GET | `/api/config` | `{deploymentMode}` for the frontend |
| POST | `/functions/v1/{projectId}/{fnId}` | Public edge function invoke (per-project rate limit, optional JWT verify) |

### Authentication

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/auth/register` | No | Public registration; the first registrant becomes platform_admin, but only with a valid one-time `setupToken` (`SETUP_TOKEN`, required on Kubernetes; a docker provisioner started without it prints a generated one to its log) |
| POST | `/api/auth/login` | No | Login (argon2id), returns bearer token |
| GET | `/api/auth/setup-status` | No | `{hasAdmin}` — used by the setup wizard |
| GET | `/api/auth/me` | Yes | Current user info |
| GET | `/api/auth/users` | Yes (admin) | List all users |
| POST | `/api/auth/users` | Yes (admin) | Create user |
| DELETE | `/api/auth/users/{id}` | Yes (admin) | Delete user |
| GET | `/api/auth/tokens` | Yes | List the caller's personal access tokens (`id`, `tokenPrefix`, `name`, `scopes`, `projectId`, `createdAt`, `expiresAt`, `lastUsed`); sessions and service tokens are not listed |
| POST | `/api/auth/tokens` | Yes | Create PAT — `{name, expiresIn?, projectId?, scopes?: ["read"\|"write"\|"admin"]}`; `expiresIn` is `30d`/`12h`/`never`, default `90d`, max `365d`; `projectId` confines the token to one project (404 if the caller cannot see it); 20 per user per hour |
| POST | `/api/auth/tokens/{hash}/rotate` | Yes (owner) | Rotate PAT — `{graceSeconds?}` (0–3600, default 0); returns the new secret once, same scopes/project binding/lifetime |
| DELETE | `/api/auth/tokens/{hash}` | Yes | Revoke PAT |

PATs expire: a request with an expired token gets `401 {"error":"token expired","code":"token_expired"}` (a missing or unknown token gets the generic 401 without a `code`). `{hash}` is the SHA-256 hex of the raw token (`echo -n "$TOKEN" | sha256sum`).

### Organizations

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/orgs` | Yes | List user's orgs |
| POST | `/api/orgs` | Yes | Create org |
| GET | `/api/orgs/{id}` | Yes | Get org details |
| PATCH | `/api/orgs/{id}` | Yes | Update org |
| DELETE | `/api/orgs/{id}` | Yes | Delete org |
| GET | `/api/orgs/{id}/members` | Yes | List org members |
| POST | `/api/orgs/{id}/members` | Yes | Invite member (email) |
| PATCH | `/api/orgs/{id}/members/{userId}` | Yes | Update member role |
| DELETE | `/api/orgs/{id}/members/{userId}` | Yes | Remove member |
| GET | `/api/orgs/{id}/invites` | Yes | List pending invites |
| GET | `/api/orgs/{id}/projects/{pid}/members` | Yes | List project members |
| POST | `/api/orgs/{id}/projects/{pid}/members` | Yes | Add project member |

### Vault

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/vault/status` | No | Vault initialized/sealed status |
| POST | `/api/vault/init` | No | Initialize vault with Shamir shares |
| POST | `/api/vault/unseal` | No | Submit unseal share |
| GET | `/api/vault/pki/public-key` | No | Get vault public key |
| POST | `/api/vault/seal` | Yes | Seal the vault |
| POST | `/api/vault/rekey` | Yes | Rekey with new shares |
| GET | `/api/vault/secrets/*` | Yes | Read secret |
| PUT | `/api/vault/secrets/*` | Yes | Write secret |
| DELETE | `/api/vault/secrets/*` | Yes | Delete secret |

### Provisioning

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| GET | `/api/provision` | Yes | List all instances |
| POST | `/api/provision` | Yes | Provision new database (K8s or Docker) |
| POST | `/api/provision/estimate` | Yes | Cost estimate |
| GET | `/api/provision/{id}` | Yes | Instance status |
| DELETE | `/api/provision/{id}` | Yes | Delete instance (purges vault paths under `projects/{id}/`). Optional body `{"confirmDeleteBackups": true}` also deletes the project's backup objects in R2/S3 after the instance is gone; absent/false keeps them |
| POST | `/api/provision/{id}/backups/purge` | Yes | Retry the backup deletion for a project left in `BACKUPS_PENDING_DELETE` (same Admin role as DELETE; 409 for a live project) |
| PATCH | `/api/provision/{id}/deletion-protection` | Yes | Toggle deletion protection |
| GET | `/api/provision/{id}/credentials` | Yes | Connection details |
| POST | `/api/provision/{id}/credentials/rotate` | Yes | Rotate credentials |
| GET | `/api/provision/{id}/logs` | Yes | Database logs; with `FEATURE_LOGRETENTION`, no further back than the plan keeps them, and `retentionHours` says how long |
| GET | `/api/provision/{id}/maintenance-window` | Yes | Get maintenance window |
| PUT | `/api/provision/{id}/maintenance-window` | Yes | Set maintenance window |
| GET | `/api/provision/{id}/metrics/current` | Yes | Real-time metrics |
| GET | `/api/provision/{id}/metrics/history` | Yes | Metrics history |
| POST | `/api/provision/{id}/backup/trigger` | Yes | Manual backup (returns BackupRef) |
| GET | `/api/provision/{id}/backup/list` | Yes | List backups for the project |
| POST | `/api/provision/{id}/backup/restore` | Yes | Async restore — returns RestoreJob with `status: RUNNING` |
| GET | `/api/provision/{id}/backup/restore/{jobId}` | Yes | Poll restore status (RUNNING / COMPLETED / FAILED) |
| POST | `/api/provision/{id}/backup/schedule` | Yes | Upsert cron schedule `{cron, retentionDays, enabled}` |
| DELETE | `/api/provision/{id}/backup/schedule` | Yes | Remove schedule (immediate cron unregister) |
| GET | `/api/provision/{id}/backup/wal-lag` | Yes | Docker mode only — seconds since last successful WAL push. K8s returns 501. |
| GET | `/api/provision/{id}/performance/summary` | Yes | pg_stat_statements summary |
| GET | `/api/provision/{id}/performance/top-queries` | Yes | Top queries by time |
| GET | `/api/provision/{id}/slow-queries/?sort=total\|mean&limit=N` | Yes | The project's own normalized statements, Postgres or MySQL (`FEATURE_SLOWQUERIES`) |
| POST | `/api/provision/{id}/slow-queries/reset` | Yes | Developer+: clear the project's statement statistics |
| POST | `/api/provision/{id}/slow-queries/enable` | Yes | Admin+: turn statistics on for a database created before the feature; restarts Postgres once |
| GET | `/api/provision/{id}/requests?range=1h\|24h\|7d` | Yes | The project's GraphQL, REST, auth and realtime request counts, error counts and p50/p95 latency, sampled every minute on Kubernetes (`FEATURE_REQUESTMETRICS`, scraping `REQUEST_METRICS_TARGETS`) |
| GET | `/api/projects/{id}/metrics` | Metrics key or session | The project's stored metrics in Prometheus text format for its own Grafana or Datadog, on Kubernetes (`FEATURE_METRICSENDPOINT`) |
| GET/POST/DELETE | `/api/projects/{id}/metrics-keys` | Yes, Admin | Metrics keys: PATs holding only `metrics:read`, bound to the project, shown once (`FEATURE_METRICSENDPOINT`) |
| GET/POST/PATCH/DELETE | `/api/projects/{id}/log-drains` | Yes, Admin | Up to 3 drains forwarding the project's database logs to Datadog or a public HTTPS endpoint; the key is write-only, in the vault (`FEATURE_LOGDRAINS`) |
| POST | `/internal/log-forwarder/pull`, `/ack` | `X-Excalibase-Forwarder-Token` | The log-forwarder pod (`excalibase-log-forwarder`, `LOG_FORWARDER_TOKEN`) takes due batches and reports each outcome |
| GET | `/api/provision/{id}/audit` | Yes | Per-project audit log |
| POST | `/api/provision/{id}/migrations` | Yes | Apply SQL migration |
| POST | `/api/provision/{id}/snapshot/export` | Yes | pg_dump export (never the `auth` schema) |
| GET | `/api/projects/{id}/info` | Yes | Combined project + org metadata (for auth-service JWT mint); carries `corsAllowedOrigins` (which excalibase-graphql reads to answer CORS per project), `requireEmailVerification` and `siteUrl` |
| GET | `/api/projects/{id}/cors` | Yes | Browser-origin allowlist for the project's data plane (Developer+). `{"allowedOrigins": [...], "allowWildcard": bool}` |
| PUT | `/api/projects/{id}/cors` | Yes | Replace the allowlist (Developer+). Absolute origins only (`scheme://host[:port]`, max 32); `"*"` needs `"allowWildcard": true` and must be the only entry. Empty = no CORS headers (browser apps blocked). See `docs/project-cors.md` |
| GET | `/api/projects/{id}/auth-settings` | Yes (Developer+) | Read the project's auth settings `{requireEmailVerification, siteUrl}` |
| PUT | `/api/projects/{id}/auth-settings` | Yes (Developer+) | Set the project's auth settings; `siteUrl` must be an absolute `http`/`https` URL with no trailing slash, or empty to unset |
| GET | `/api/projects/{id}/auth-providers` | Yes (Developer+) | End-user Google/GitHub sign-in settings and the callback URL to register; the client secret is never returned (flag `authoauth`) |
| PUT | `/api/projects/{id}/auth-providers/{provider}` | Yes (Admin) | Set `{enabled, clientId, clientSecret?}`; the secret goes to the vault, an empty one keeps the stored secret (flag `authoauth`) |
| — | `/api/projects/{id}/auth-settings` with flag `authotp` | Yes (Admin writes) | Adds `emailOtpEnabled` (sign-in by emailed link or eight-digit code) and the shared `redirectUrls` + `allowNewUsers`; `/info` then carries `emailOtpEnabled` for the auth service. The mail relay sends `magic_link`/`email_otp`, and `link_magic_link`/`link_email_otp` for an address a signed-in user adds to their account (they say so, and not "sign in"), only while the flag and the project's switch are on, names the project and its site in each, and caps a project at 1000 a day (`sign_in_mail_sends`) |
| GET/PATCH/DELETE | `/api/projects/{id}/end-users/{userId}` | Yes, Admin | One end user's details (`fullName`, `userMetadata`, `appMetadata`, ...); PATCH takes only `{appMetadata?, userMetadata?, fullName?}` (metadata merges, a key set to `null` is removed), DELETE removes the account; both audited, the update by field name only (flag `authuser`) |
| — | `/api/projects/{id}/auth-settings` with flag `authguest` | Yes (Admin writes) | Adds `guestSignInEnabled` (guest accounts: no address, role `guest`) and `guestCaptchaSecretSet`, with the shared `allowNewUsers`; `/info` then carries `guestSignInEnabled`, `guestCaptchaEnabled` (only when a Turnstile secret is set) and `stableSubject: true`, which makes the auth service sign `sub` as the account id |
| PUT/DELETE | `/api/projects/{id}/auth-settings/guest-captcha` | Yes (Admin) | Set `{secretKey}` or remove the guest Turnstile secret; it goes to the vault at `projects/{id}/auth/captcha` and is never returned; audited (flag `authguest`) |
| — | `/api/projects/{id}/info` and the mail relay with flag `authuser` | service | `/info` carries `userApiEnabled: true`; the relay sends `email_change` (to the new address; only the signed-in account that asked can confirm it) and `email_change_notice` (to the old one; a password change cancels the change). `identity_linked` (a sign-in method was added) goes out while `authoauth` is on with an enabled provider, or `authotp` is on with the project's switch. All three draw on an account-mail budget of 1000 a day per project, counted apart from sign-in mail (`sign_in_mail_sends.kind`) |
| — | `/api/admin/tiers` with flag `appgitbuilds` | Yes (platform admin writes) | Adds each plan's app build limits (EXC-547): `appBuildCpuMillis`, `appBuildMemory`, `appBuildDisk`, `appBuildTimeoutSeconds`, `appBuildConcurrency`, `appBuildMaxImage`, `appBuildMaxPerHour` (Free 2 CPU / 4Gi / 10Gi / 15 min / 1 / 2Gi / 30; Standard 4 CPU / 8Gi / 20Gi / 30 min / 2 / 5Gi / 120). While the flag is off they are not shown and an edit leaves them as they are |
| GET, PUT, DELETE | `/api/projects/{projectId}/apps/{appId}/git` (flag `appgitbuilds`; DELETE answers whatever the flag and site git builds) | Yes (Developer+ writes) | An app's git connection (EXC-547): PUT `{repositoryId \| repository, settings: {branch, builder: auto\|dockerfile\|buildpacks, rootDir, dockerfilePath, buildArgs}}` connects a repository the caller's own GitHub sign-in shows them and queues a first build; GET answers `{connected, connection, builders}`, with build argument values only for Developer+ (below, names only, also in the build list); DELETE forgets it and cancels the app's builds |
| POST, GET | `/api/projects/{projectId}/apps/{appId}/git/sign-in`, `/git/repositories` (flag `appgitbuilds`) | Yes (Developer+) | The sites' GitHub sign-in started from an app (the callback returns to the app's page), and the repositories that sign-in showed |
| GET, POST | `/api/projects/{projectId}/apps/{appId}/builds` (flag `appgitbuilds`) | Yes (Developer+ to rebuild) | The app's git builds, newest first; POST rebuilds the branch head (202), counted against the plan's `appBuildMaxPerHour` (429 with `Retry-After` past it) |
| GET | `/api/projects/{projectId}/apps/{appId}/builds/{buildId}` (flag `appgitbuilds`) | Yes (Developer+) | One build with its log; build-visible secret values are redacted |
| POST | `/api/projects/{projectId}/internal-registry/pull-credential/rotate` (flag `appgitbuilds`) | Yes (project Admin) | Retires the project's pull credential for the platform's own registry (EXC-540); each app pulls with the new one from its next deploy |
| GET, POST | `/internal/registry/token` (flag `appgitbuilds`, internal listener only) | Project pull credential | The internal registry's token service (Docker token spec): a two-minute pull token on the credential's own project. Images on the platform registry deploy only from git builds, by digest |

### Edge Functions (per-project Deno runtime)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/projects/{projectId}/functions` | List functions |
| POST | `/api/projects/{projectId}/functions` | Create function (multi-file source) |
| GET | `/api/projects/{projectId}/functions/runtime/status` | Runtime health |
| GET | `/api/projects/{projectId}/functions/secrets` | List secret keys (values write-only) |
| POST | `/api/projects/{projectId}/functions/secrets` | Set/update secret(s) |
| DELETE | `/api/projects/{projectId}/functions/secrets/{key}` | Delete a secret |
| GET | `/api/projects/{projectId}/functions/{fnId}` | Get function source |
| DELETE | `/api/projects/{projectId}/functions/{fnId}` | Delete function |
| POST | `/api/projects/{projectId}/functions/{fnId}/invoke` | Admin test invoke |
| GET | `/api/projects/{projectId}/functions/{fnId}/logs` | Stream logs (SSE) |
| POST | `/functions/v1/{projectId}/{fnId}` | **Public** Supabase-style invoke |

### Realtime (per-table CDC publication)

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/projects/{projectId}/realtime/tables` | List published tables |
| PUT | `/api/projects/{projectId}/realtime/tables/{schema}/{table}` | Add table to publication |
| DELETE | `/api/projects/{projectId}/realtime/tables/{schema}/{table}` | Remove table |
| POST | `/api/projects/{projectId}/realtime/enable-all` | Add all user tables |
| POST | `/api/projects/{projectId}/realtime/disable-all` | Remove all tables |

### Setup, Alerts & Parameter Groups

| Method | Endpoint | Description |
|--------|----------|-------------|
| `*` | `/api/setup/*` | Vault init wizard + first-admin creation |
| `*` | `/api/alerts/*` | Alert rules + active/historical alerts |
| `*` | `/api/parameter-groups/*` | Reusable Postgres parameter groups |

### Schema Management

All schema endpoints require auth and an unsealed vault. The `?schema=` query parameter defaults to `public`.

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/schema/{projectId}/tables` | List tables |
| POST | `/api/schema/{projectId}/tables` | Create table |
| PATCH | `/api/schema/{projectId}/tables/{name}` | Rename/alter table |
| DELETE | `/api/schema/{projectId}/tables/{name}` | Drop table |
| GET | `/api/schema/{projectId}/tables/{name}/columns` | List columns |
| POST | `/api/schema/{projectId}/tables/{name}/columns` | Add column |
| GET | `/api/schema/{projectId}/tables/{name}/rows` | Query rows (paginated) |
| POST | `/api/schema/{projectId}/tables/{name}/rows` | Insert row |
| PATCH | `/api/schema/{projectId}/tables/{name}/rows` | Update row |
| DELETE | `/api/schema/{projectId}/tables/{name}/rows` | Delete row |
| GET | `/api/schema/{projectId}/roles` | List roles |
| POST | `/api/schema/{projectId}/roles` | Create role |
| DELETE | `/api/schema/{projectId}/roles/{name}` | Drop role |
| GET | `/api/schema/{projectId}/extensions` | List extensions |
| GET | `/api/schema/{projectId}/policies` | List RLS policies |
| GET | `/api/schema/{projectId}/functions` | List functions |
| GET | `/api/schema/{projectId}/triggers` | List triggers |
| POST | `/api/schema/{projectId}/query` | Execute SQL query |
| POST | `/api/schema/{projectId}/ddl` | Execute DDL |
| GET | `/api/schema/{projectId}/advisors/performance` | Performance advisor |
| GET | `/api/schema/{projectId}/advisors/security` | Security advisor |

**Example — query rows with filters:**
```bash
curl "http://localhost:24005/api/schema/my-db/tables/users/rows?limit=50&offset=0&sort=id&order=asc&filter[email]=ilike:alice" \
  -H "Authorization: Bearer $TOKEN"
```

**Response format:**
```json
{
  "columns": [{"name": "id", "dataType": "INT4"}, {"name": "email", "dataType": "VARCHAR"}],
  "rows": [[1, "alice@test.com"], [2, "bob@test.com"]],
  "totalCount": 2
}
```

### Error Response Format

All errors return JSON:
```json
{"error": "descriptive message", "status": 400}
```
Error messages are sanitized — PostgreSQL internal details are stripped from 500 responses.

## Environment Variables

**Core**

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `24005` | Server port |
| `STORAGE_PATH` | `../provisioning-data` | Data directory |
| `LOG_LEVEL` | `debug` | Log level |
| `CORS_ORIGINS` | `https://app.excalibase.io` | Comma-separated allowed origins (required, server refuses to start if empty) |
| `DEPLOYMENT_MODE` | `selfhosted` | `selfhosted` or `cloud`. Cloud requires `PLATFORM_DB_URL` and enables tier enforcement + multi-org. |
| `PUBLIC_BASE_URL` | `https://api.excalibase.io` | Base URL emitted in SDK snippets and function invoke URLs |
| `PUBLIC_PORT` | **required** | Listener only the edge reaches (e.g. `24006`); must differ from `PORT`. `PORT` serves in-cluster callers and ignores X-Forwarded-For |
| `TRUSTED_PROXY_CIDRS` | **required** | The edge's addresses; the public listener believes X-Forwarded-For only from them |
| `STUDIO_URL` | **required** | Studio origin that verification and password-reset emails link to; the server refuses to start without it |
| `AUTH_INTERNAL_URL` | | excalibase-auth inside the platform network (e.g. `http://auth:24000`); Studio's SDK key routes call it and answer 503 without it |

**Storage**

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_PATH` | `../provisioning-data/excalibase.db` | SQLite path (self-hosted) |
| `PLATFORM_DB_URL` | | Postgres DSN (required for cloud mode) |

**Vault**

| Variable | Default | Description |
|----------|---------|-------------|
| `VAULT_URL` | | Remote vault HTTP URL — when set, the server skips in-process vault and `/api/vault/*` is not mounted |
| `VAULT_PAT` | | PAT for remote vault auth |

**Kubernetes**

| Variable | Default | Description |
|----------|---------|-------------|
| `KUBECONFIG_PATH` | | Explicit kubeconfig file |
| `KUBE_API_URL` | | Remote K8s API URL (out-of-cluster deployments) |
| `KUBE_BEARER_TOKEN` | | ServiceAccount token for the remote API |
| `KUBE_CA_CERT` | | PEM-encoded CA cert |
| `KUBE_INSECURE_SKIP_VERIFY` | | `true` to skip TLS verify (dev only) |
| `WATCHER_CHART_PATH` | `/charts/excalibase-watcher` | Helm chart path for CDC watcher |

**Docker provisioner** — production peer of K8s mode. Operationally simpler (no operator, no CRDs, no etcd) but with real trade-offs:

- The provisioner needs **root-equivalent access** to the Docker daemon (`docker` group ≈ root). Podman has the same constraint unless explicitly running rootless. K8s mode by contrast uses a least-privilege ServiceAccount + Role.
- All projects share the daemon's process namespace — there is no equivalent of K8s namespaces / NetworkPolicies enforced by the platform.
- Single-host *Kubernetes* installs (k0s, k3s, RKE2, MicroK8s — including rootless) are also production-grade; Docker mode is one option, not the only single-host option.

Works against a local socket *or* a remote Docker daemon over TLS, same setup pattern as Dokploy / CapRover.

| Variable | Default | Description |
|----------|---------|-------------|
| `PROVISIONER_MODE` | `k8s` | `k8s` (CNPG) or `docker` (containers on a Docker daemon) |
| `DOCKER_HOST` | | Docker URI: `unix:///var/run/docker.sock` (local) or `tcp://host:2376` (remote, requires TLS) |
| `DOCKER_CERT_PATH` | | TLS cert directory (`ca.pem`, `cert.pem`, `key.pem`) for remote daemon |
| `DOCKER_TLS_VERIFY` | | Non-empty enables TLS verify (required for remote daemons) |

**Edge functions**

| Variable | Default | Description |
|----------|---------|-------------|
| `DENO_RUNTIME_URL` | `http://deno-runtime.serverless.svc.cluster.local:8000` | Deno runtime endpoint |
| `DENO_RUNTIME_SECRET` | | Shared secret for Deno runtime auth |
| `DENO_NAMESPACE` | `serverless` | K8s namespace for Deno pods |
| `DENO_RUNTIME_IMAGE` | `excalibase/deno-runtime:latest` | Per-project Deno pod image |
| `EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS` | | Operator-level outbound allowlist merged into every project's edge-function egress: comma-separated `host`, `host:port` or `*.suffix` entries in Deno `--allow-net` form (e.g. `*.excalibase.io,api.resend.com`). Empty = projects start with no egress and opt hosts in via `PUT /api/projects/{id}/functions/egress`. Private / cluster-internal addresses are refused; a malformed list stops the server at boot. See `docs/functions-egress.md`. |

**Pooler / CDC**

| Variable | Default | Description |
|----------|---------|-------------|
| `NATS_URL` | | NATS server for policy-change events + CDC fan-out |
| `REALTIME_PUBLICATION_NAME` | `cdc_watcher_pub` | Publication name; must match watcher daemon config |

## Testing

```bash
cd server-go

# Unit tests (fast, excludes k8s integration)
go test $(go list ./internal/... | grep -v /k8s) -race -cover

# Postgres integration tests (needs Docker)
go test -tags=integration ./internal/storage/postgres/... -race -v -timeout 5m

# Full suite including k3s (needs Docker, ~5min)
go test ./internal/... -race -cover -timeout 7m

# Frontend E2E
cd frontend && npx playwright test
```

16 Go packages (84 test files), ~25 Postgres integration tests, ~45 k8s tests, 25 Playwright E2E spec files (~115 tests, including Docker-mode flows). Integration tests use testcontainers-go (real PostgreSQL + k3s in Docker).

## Tech Stack

- **Backend**: Go 1.26.10+, chi router, client-go, Docker SDK, Helm SDK, NATS client, esbuild
- **Storage**: SQLite (self-hosted) / PostgreSQL via CNPG (cloud), auto-migrate on startup
- **Vault**: in-process Shamir on the platform Postgres, or standalone HTTP service via `VAULT_URL`
- **Auth**: argon2id password hashing, PATs (`excali_…` prefix), JWT minted via separate auth service
- **Edge Functions**: Deno workers, esbuild bundling, public Supabase-style invoke
- **Realtime**: PostgreSQL logical replication via watcher daemon, per-table `ALTER PUBLICATION` toggle
- **Frontend**: React 18, Vite, Tailwind CSS, TanStack Query/Table, CodeMirror 6, cmdk
- **K8s Operators**: CloudNativePG (PostgreSQL, DocumentDB), MOCO (MySQL)
- **Metrics**: Kubernetes metrics-server (CPU/memory), CNPG Prometheus exporter
- **Backup**: Barman Cloud plugin (per-project `ObjectStore`) → S3-compatible store (Cloudflare R2 in production; endpoint/bucket/creds from `BACKUP_DEFAULT_*` or vault `backup/s3`); restore reads from the same configured store
- **Testing**: httptest, testcontainers-go (k3s + PostgreSQL), Playwright, Vitest

## License

Apache 2.0
