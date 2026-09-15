# Excalibase Provisioning

Kubernetes-native database provisioning platform. Built with Go + client-go.

## What It Does

One API call provisions a production PostgreSQL cluster with backup, monitoring, and performance insights:

```bash
curl -X POST http://localhost:24005/api/provision \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"projectName":"my-db","orgId":"myorg","databaseType":"POSTGRESQL","tier":"STANDARD"}'
```

- **9-stage provisioning pipeline** — namespace, CRD deployment, pod readiness, credential extraction, backup config, metrics setup, watcher deployment, role creation
- **Three deployment modes per project** — Kubernetes (CNPG operator on any distro: EKS/GKE/AKS, RKE2, or single-node k0s/k3s/MicroK8s including rootless), Docker (local socket or remote daemon over TLS, Dokploy/CapRover-style), or BYOC (register an external DB you already manage). K8s vs Docker is a real trade-off: K8s provides ServiceAccount + Role + namespace isolation, Docker is operationally simpler but daemon access is root-equivalent and there's no per-project isolation enforced by the platform.
- **Self-hosted or cloud** — `DEPLOYMENT_MODE=selfhosted` (Postgres store + Postgres vault, single org) or `cloud` (Postgres store + Postgres vault or remote HTTP vault, multi-tenant + tier enforcement)
- **3 tiers** — FREE (1 pod), STANDARD (3 pods + HA), ENTERPRISE (5 pods)
- **Real metrics** — per-pod CPU/memory from metrics-server + CNPG database metrics from port 9187
- **Backup & PITR** — automated WAL archiving + scheduled backups + point-in-time recovery
- **Performance insights** — pg_stat_statements: top queries, cache hit ratio, wait events
- **Schema management** — full SQL schema introspection, CRUD for tables/columns/roles/extensions/policies/functions/triggers/indexes
- **Realtime** — per-table CDC publication toggle (ALTER PUBLICATION) backed by a watcher daemon
- **Edge functions** — per-project Deno workers with multi-file bundling (esbuild), secrets, log streaming, public invoke at `/functions/v1/{projectId}/{name}`
- **Vault** — Shamir secret sharing for credentials, AES-256-GCM at rest; runs in-process (Postgres-backed) or as a standalone HTTP service

## Architecture

```
server-go/       Go backend (chi router, client-go, Docker SDK)
├── cmd/server/    Entry point, router wiring, mode selection
├── internal/
│   ├── handler/     HTTP handlers (auth, orgs, provisioning, BYOC, schema,
│   │                edge functions, realtime, setup, vaultapi, etc.)
│   ├── service/     Business logic (provisioning, metrics, backup, migrations,
│   │                snapshots, alerting, PgDog notifier)
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

## Quick Start

### Prerequisites

- For K8s mode: any cluster — managed (EKS/GKE/AKS), self-hosted (RKE2), or single-node (k0s/k3s/MicroK8s, rootless OK). For Docker mode: a Docker daemon (local or remote over TLS).
- Go 1.24+
- Node.js 18+ (for frontend)

### 1. Install operators

```bash
# CloudNativePG
kubectl apply --server-side -f \
  https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.0.yaml

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
PORT=24005 STORAGE_PATH=../provisioning-data ./excalibase-server
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

# Register the first admin (auto-promoted to platform_admin in self-hosted mode)
curl -s -X POST http://localhost:24005/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "<password>", "email": "admin@example.com"}'

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
    "projectName": "duke-database",
    "orgId": "exca",
    "databaseType": "POSTGRESQL",
    "tier": "STANDARD",
    "backup": {"enabled": true, "schedule": "0 2 * * *", "retention": 30},
    "parameters": {"shared_preload_libraries": "pg_stat_statements"}
  }'
```

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
| POST | `/api/auth/register` | No | Public registration; first registrant becomes platform_admin |
| POST | `/api/auth/login` | No | Login (argon2id), returns bearer token |
| GET | `/api/auth/setup-status` | No | `{hasAdmin}` — used by the setup wizard |
| GET | `/api/auth/me` | Yes | Current user info |
| GET | `/api/auth/users` | Yes (admin) | List all users |
| POST | `/api/auth/users` | Yes (admin) | Create user |
| DELETE | `/api/auth/users/{id}` | Yes (admin) | Delete user |
| GET | `/api/auth/tokens` | Yes | List personal access tokens (`tokenPrefix`, `scopes`, `projectId`, `expiresAt`, `lastUsed`) |
| POST | `/api/auth/tokens` | Yes | Create PAT — `{name, expiresIn?, projectId?, scopes?: ["read"\|"write"\|"admin"]}`; `expiresIn` is `30d`/`12h`/`never`, default `90d`, max `365d`; `projectId` confines the token to one project (404 if the caller cannot see it) |
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
| POST | `/api/provision/byoc` | Yes | Register an externally managed database (no provisioning) |
| POST | `/api/provision/estimate` | Yes | Cost estimate |
| GET | `/api/provision/{id}` | Yes | Instance status |
| DELETE | `/api/provision/{id}` | Yes | Delete instance (purges vault paths under `projects/{id}/`). Optional body `{"confirmDeleteBackups": true}` also deletes the project's backup objects in R2/S3 after the instance is gone; absent/false keeps them |
| POST | `/api/provision/{id}/backups/purge` | Yes | Retry the backup deletion for a project left in `BACKUPS_PENDING_DELETE` (same Admin role as DELETE; 409 for a live project) |
| PATCH | `/api/provision/{id}/deletion-protection` | Yes | Toggle deletion protection |
| GET | `/api/provision/{id}/credentials` | Yes | Connection details |
| POST | `/api/provision/{id}/credentials/rotate` | Yes | Rotate credentials |
| GET | `/api/provision/{id}/logs` | Yes | Pod logs |
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
| GET | `/api/provision/{id}/audit` | Yes | Per-project audit log |
| POST | `/api/provision/{id}/migrations` | Yes | Apply SQL migration |
| POST | `/api/provision/{id}/snapshot/export` | Yes | pg_dump export |
| GET | `/api/projects/{id}/info` | Yes | Combined project + org metadata (for auth-service JWT mint); carries `corsAllowedOrigins` (which excalibase-graphql reads to answer CORS per project), `requireEmailVerification` and `siteUrl` |
| GET | `/api/projects/{id}/cors` | Yes | Browser-origin allowlist for the project's data plane (Developer+). `{"allowedOrigins": [...], "allowWildcard": bool}` |
| PUT | `/api/projects/{id}/cors` | Yes | Replace the allowlist (Developer+). Absolute origins only (`scheme://host[:port]`, max 32); `"*"` needs `"allowWildcard": true` and must be the only entry. Empty = no CORS headers (browser apps blocked). See `docs/project-cors.md` |
| GET | `/api/projects/{id}/auth-settings` | Yes (Developer+) | Read the project's auth settings `{requireEmailVerification, siteUrl}` |
| PUT | `/api/projects/{id}/auth-settings` | Yes (Developer+) | Set the project's auth settings; `siteUrl` must be an absolute `http`/`https` URL with no trailing slash, or empty to unset |

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

**BYOC (bring-your-own Postgres)**

| Variable | Default | Description |
|----------|---------|-------------|
| `BYOC_EGRESS_ALLOWLIST` | | Optional egress allowlist for BYOC targets: comma-separated CIDRs, IPs, hostnames or `*.suffix` wildcards (e.g. `203.0.113.0/24, *.rds.amazonaws.com`). Empty = any public address. Loopback, RFC-1918, link-local, ULA, CGNAT and cloud-metadata ranges are always refused, and the same check runs at every dial (DNS rebinding is refused at connect time). A malformed list stops the server at boot. See `server-go/internal/byoc`. |

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
| `NATS_URL` | | NATS server for PgDog reload signals + CDC fan-out |
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

16 Go packages (84 test files), ~25 Postgres integration tests, ~45 k8s tests, 26 Playwright E2E spec files (~120 tests, including BYOC and Docker-mode flows). Integration tests use testcontainers-go (real PostgreSQL + k3s in Docker).

## Tech Stack

- **Backend**: Go 1.24+, chi router, client-go, Docker SDK, Helm SDK, NATS client, esbuild
- **Storage**: SQLite (self-hosted) / PostgreSQL via CNPG (cloud), auto-migrate on startup
- **Vault**: in-process Shamir on the platform Postgres, or standalone HTTP service via `VAULT_URL`
- **Auth**: argon2id password hashing, PATs (`excali_…` prefix), JWT minted via separate auth service
- **Connection Pooler**: PgDog fork (Postgres-backed config + NATS reload)
- **Edge Functions**: Deno workers, esbuild bundling, public Supabase-style invoke
- **Realtime**: PostgreSQL logical replication via watcher daemon, per-table `ALTER PUBLICATION` toggle
- **Frontend**: React 18, Vite, Tailwind CSS, TanStack Query/Table, CodeMirror 6, cmdk
- **K8s Operators**: CloudNativePG (PostgreSQL), Vitess (MySQL planned), MongoDB Community (planned)
- **Metrics**: Kubernetes metrics-server (CPU/memory), CNPG Prometheus exporter
- **Backup**: barmanObjectStore → S3-compatible store (Cloudflare R2 in production; endpoint/bucket/creds from `BACKUP_DEFAULT_*` or vault `backup/s3`); restore reads from the same configured store
- **Testing**: httptest, testcontainers-go (k3s + PostgreSQL), Playwright, Vitest

## License

Apache 2.0
