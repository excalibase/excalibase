# CLAUDE.md

## Project Overview

Excalibase Provisioning — database provisioning platform written in Go. Provisions PostgreSQL in **two production-grade modes**: Kubernetes (CloudNativePG operator — works against any distro from EKS/GKE/AKS down to single-node k0s/k3s/RKE2) or Docker (containers on a local or remote Docker daemon, Dokploy/CapRover-style). Also supports BYOC — registering an externally managed database without provisioning. Includes org/project RBAC, PgDog connection pooler integration, edge functions runtime, realtime publication management, and a React studio.

**Mode trade-offs (deliberate user choice):**
- **K8s mode** has a real auth/isolation surface — the provisioner runs with a least-privilege ServiceAccount + Role, projects get separate namespaces, NetworkPolicies are available. More moving parts to operate, but tighter security posture.
- **Docker mode** is operationally simpler (no operator, no CRDs, no etcd) but the provisioner needs root-equivalent access to the Docker daemon (Podman has the same constraint unless explicitly running rootless). All projects share the daemon's process namespace — there is no per-project isolation enforced by the platform itself.

## Build & Run

```bash
# Build Go server
cd server-go && go build -o excalibase-server ./cmd/server/

# Self-hosted mode (default) — Postgres platform DB + Postgres vault, single default org, no tier enforcement
PORT=24005 PLATFORM_DB_URL=postgres://platform:pass@localhost:5432/platform STORAGE_PATH=../provisioning-data CORS_ORIGINS=http://localhost:5173 ./excalibase-server

# Cloud mode — Postgres platform DB + Postgres-backed vault, multi-tenant, tier enforcement
DEPLOYMENT_MODE=cloud PLATFORM_DB_URL=postgres://platform:pass@localhost:5432/platform ./excalibase-server

# Docker provisioner mode (Postgres in containers instead of CNPG clusters)
PROVISIONER_MODE=docker DOCKER_HOST=unix:///var/run/docker.sock ./excalibase-server

# Remote vault service (HTTP) instead of in-process Postgres-backed vault
VAULT_URL=http://vault:8200 VAULT_PAT=excali_... ./excalibase-server

# Run tests (fast, unit only — excludes k8s integration)
go test $(go list ./internal/... | grep -v /k8s) -race -cover

# Run all tests including k3s + Postgres integration
go test -tags=integration ./internal/... -race -cover -timeout 7m

# Frontend
cd frontend && npm install && npm run dev

# Playwright E2E (26 spec files, ~120 tests)
cd frontend && npx playwright test
```

## Architecture

### Go Backend (server-go/)

Clean architecture with strict layer separation:

```
Handler (HTTP) → Service (business logic) → Provisioner (strategy) → K8s Client (client-go)
                                                                  → Docker Client
                                           → Storage (Postgres)
                                           → Vault (in-process Postgres-backed or HTTP)
                                           → PgDog Notifier (NATS)
                                           → Edge Function Runtime (Deno HTTP)
```

**Key principle**: All K8s operations use native `client-go` — no kubectl shell-outs. All Docker operations use the Docker SDK — no shell-outs.

### Project Structure

```
server-go/
├── cmd/server/main.go           # Entry point, router wiring, mode selection
├── internal/
│   ├── auth/                    # JWT, argon2id passwords, 3-level RBAC
│   ├── config/                  # AppConfig, tier config, IS_CLOUD flag
│   ├── domain/                  # Entities, DTOs, enums (DeploymentMode, etc.)
│   ├── handler/                 # HTTP handlers (chi router)
│   │   └── vaultapi/            # Subrouter for the standalone vault service
│   ├── service/                 # Business logic (provisioning, metrics, backup,
│   │                            #   migrations, snapshot, alerting, PgDog notifier)
│   ├── provisioner/             # Strategy: PostgreSQL (CNPG), Docker; Factory dispatch
│   ├── k8s/                     # client-go wrapper, CRD builders, Helm SDK, mock
│   ├── edgefn/                  # Edge function store + Deno runtime client + esbuild
│   ├── storage/
│   │   ├── sqlite/              # SQLite store (self-hosted)
│   │   └── postgres/            # Postgres store (cloud)
│   ├── security/                # AES-256-GCM for filesystem state
│   ├── middleware/              # CORS, security headers, TenantContext
│   └── vault/                   # In-process Shamir vault (Postgres + AES-256-GCM)
charts/
├── platform-base/               # CNPG platform-db cluster (3 instances, S3 backup)
├── platform-aio/                # All-in-one chart bundling provisioning + deps
└── provisioning/                # Provisioning server (ingress, RBAC, PDB)
deno-server/                     # Deno edge functions runtime (Web Workers, sandboxed)
frontend/                        # React 18 studio (Vite, Tailwind, TanStack)
```

### Deployment Modes (per project)

`DeploymentMode` on `DatabaseInstance` controls how a project is provisioned:
- **K8s** — full 9-stage pipeline against CNPG. Works on any distro (managed EKS/GKE/AKS, self-hosted RKE2, or single-node k0s/k3s/MicroK8s — including rootless). Provisioner authenticates as a ServiceAccount with a least-privilege ClusterRole; projects are namespace-isolated. Default when `PROVISIONER_MODE=k8s`.
- **Docker** — runs Postgres as a container on a Docker daemon (local socket *or* a remote daemon over TLS, Dokploy/CapRover-style). Simpler to operate, but requires root-equivalent access to the daemon (same constraint applies to Podman unless explicitly rootless). Per-project isolation is process-level only — there is no equivalent of K8s namespaces / NetworkPolicies. `PROVISIONER_MODE=docker`. Backup/restore/PITR landed via WAL-G sidecar (May 2026) — see `OPERATOR.md` §6 for ops; `DOCKER_BACKUP_PLAN.md` is the historical design doc.
- **BYOC** — `POST /api/provision/byoc` registers an externally managed DB; no provisioning, just stores credentials in vault and creates the instance row. Every BYOC target goes through `internal/byoc` (SSRF guard): internal IPv4/IPv6 ranges + cloud metadata refused, host must be a single hostname/IP (no multi-host, `hostaddr` or DSN-field injection), the guard is also the lib/pq dialer for BYOC projects so a rebound DNS name is refused at connect time, and `BYOC_EGRESS_ALLOWLIST` optionally pins targets to operator-approved CIDRs/hostnames. Threat model in `internal/byoc/doc.go`.

### Self-hosted vs Cloud (platform-wide)

`DEPLOYMENT_MODE` env var (`selfhosted` default, or `cloud`):
- **Self-hosted**: Postgres store + Postgres-backed vault, default org auto-created at first registration, no tier enforcement, single tenant
- **Cloud**: Postgres store + a vault that is one of (in priority): remote HTTP (`VAULT_URL` set), Postgres-backed in-process (init/unseal by the bootstrap Job). Multi-org create/delete, tier limits enforced. See `cmd/server/vault_wiring.go` and `buildVault` in `cmd/server/main.go`.

### Strategy Pattern for Database Provisioning

`DatabaseProvisioner` interface with two implementations:
- `PostgreSQLProvisioner` — CloudNativePG operator (full 9-stage pipeline)
- `DockerProvisioner` — Docker SDK, single-container Postgres
- MySQL/MongoDB — planned

`Factory` selects the provisioner based on `DatabaseType`. The K8s vs Docker split is decided at startup via `PROVISIONER_MODE` and the resulting client wired into the factory.

### 9-Stage Provisioning Pipeline (PostgreSQL on K8s)

1. VALIDATING → 2. NAMESPACE_CREATION → 3. CRD_DEPLOYMENT → 4. WAITING_FOR_READY
5. CREDENTIAL_GENERATION → 6. BACKUP_CONFIGURATION → 7. METRICS_SETUP
8. WATCHER_DEPLOYMENT → 9. ROLE_CREATION → 10. COMPLETED

Stages register rollback compensations into a `ProvisionContext`; failure runs them in LIFO order. The Docker mode condenses to: container create → wait for `pg_isready` → create roles via `docker exec`.

After `ROLE_CREATION` the provisioner publishes vault entries at `projects/{orgSlug}/{projectId}/credentials/{role}` and (if PgDog is configured) registers the cluster.

`Deprovision` reverses the chain: PgDog deregister → operator/container teardown → vault path purge under `projects/{orgSlug}/{projectId}/` → store delete. The vault purge is critical for BYOC (live external credentials).

### Kubernetes Integration

Uses `KubeClient` interface for testability:
- `Client` — real client-go implementation
- `MockClient` — in-memory test double

Connection priority (in `k8s.NewClient`): explicit API URL → kubeconfig path → `KUBECONFIG` env → in-cluster → `~/.kube/config`. Supports remote API with `KUBE_API_URL` + `KUBE_BEARER_TOKEN` + `KUBE_CA_CERT`.

CRDs built as `unstructured.Unstructured` objects via `BuildPostgreSQLCluster()`. Helm SDK for deploying per-project watchers.

### Storage

Postgres-only storage in both modes (`PLATFORM_DB_URL` always required): the CNPG platform-db cluster (or any Postgres), auto-migrates on startup. The `PlatformStore` interface (InstanceStore + UserStore + TokenStore + OrgStore + PgDogConfigStore + AuditLogStore) is implemented by `internal/storage/postgres`; the same connection backs the vault tables (`vault_barrier`, `vault_secrets`).

### Vault

Two deployment options, both implement the same `VaultClient` interface:
- **In-process** (default) — Shamir secret sharing on the platform Postgres (`vault_barrier` / `vault_secrets`). On k8s the chart bootstrap Job inits/unseals it in every deployment mode and keeps the share in the `platform-bootstrap` Secret; on the docker provisioner the binary auto-inits with one share persisted to `STORAGE_PATH/unseal.key` (or honours `VAULT_UNSEAL_KEY`). Setup wizard at `/setup` creates the first admin.
- **Standalone HTTP** — set `VAULT_URL` + `VAULT_PAT` and the server skips local vault init; the local server's `/api/vault/*` routes are not mounted.

Vault paths are org-scoped: `projects/{orgSlug}/{projectId}/credentials/{role}`. Backup S3 creds at `backup/s3`. Vault setup wizard handles share generation + first-admin creation atomically.

**Backup credential flow at runtime** (read this if you're touching backup or vault code — the actual wiring is two-step and easy to misread):

1. Admin uploads R2/S3 master creds to vault path `backup/s3` once.
2. On project provision, `service/provisioning.go:268` reads vault → fills `req.Backup.S3` in memory.
3. `provisioner/postgresql.go:40` writes those creds to a **project-namespace-scoped** K8s secret named `backup-s3-creds` in `excalibase-{org}-{projectId}`. Each project gets its own copy.
4. `k8s/crd_builder.go:223` references that secret from the per-project CNPG cluster CRD's `barmanObjectStore.s3Credentials`. CNPG operator reads only the project-namespace secret — never vault.

If the vault is sealed/uninitialised, the platform falls back to env-driven `service.BackupDefaults` populated from the cluster-scoped `r2-creds` K8s secret. Functional but worse for security: the master creds are visible to anyone with cluster-scoped `get secret` rights. Initialise the vault and store `backup/s3` there to tighten.

Rotation: `vault put backup/s3 ...` rotates the source. New provisions pick up the new value; existing per-project K8s secrets retain the old value until re-provisioned. No auto-rotation today.

### Connection Pooler (PgDog)

Centralized PgDog pooler routes client connections to per-project CNPG clusters:
- Config stored in `pgdog_databases` / `pgdog_users` tables in platform-db
- NATS-triggered reload — provisioner publishes `pgdog.config.reload` after cluster creation
- Read/write splitting: SELECTs → replicas, writes → primary
- Fork repo: github.com/excalibase/pgdog
- Wired only when both `PLATFORM_DB_URL` and `NATS_URL` are set

### Edge Functions

Per-project Deno workers, Supabase-style:
- Storage: `internal/edgefn/store.go` (filesystem) for code + secrets
- Bundler: esbuild compiles multi-file TS bundles before sending to runtime
- Runtime: HTTP request to `DENO_RUNTIME_URL` (auth via `DENO_RUNTIME_SECRET`); returns logs + status
- Public invoke: `POST /functions/v1/{projectId}/{fnId}` with optional `verifyJwt` gating
- Per-project rate limit on the public path
- K8s mode: each project gets a dedicated Deno pod (image `DENO_RUNTIME_IMAGE`, namespace `DENO_NAMESPACE`)
- Realtime log streaming via SSE on `GET /api/projects/{projectId}/functions/{fnId}/logs`

### Realtime Publication Management

Studio surfaces a per-table CDC toggle by ALTERing the `cdc_watcher_pub` PostgreSQL publication. Handler at `/api/projects/{projectId}/realtime` connects as `excalibase_app` (the publication owner — no superuser escalation) and exposes:
- `GET /tables` — current publication membership
- `PUT/DELETE /tables/{schema}/{table}` — toggle one table
- `POST /enable-all` / `POST /disable-all`

Publication name is configurable via `REALTIME_PUBLICATION_NAME` (default `cdc_watcher_pub` — must match the watcher daemon's `publication_name` config and graphql's `app.realtime.publication-name`).

### RBAC

Three-level role system:
- **Platform**: platform_admin, platform_operator, platform_viewer, user
- **Org**: owner, admin, developer, viewer
- **Project**: admin, editor, viewer

Permissions wired through `auth.RequirePermission(...)` middleware. Org membership and project membership are independent — a project member doesn't need org membership.

### Tier Configuration

| Tier | MaxProjects | Instances | Storage | Memory | CPU | Backup |
|------|------------|-----------|---------|--------|-----|--------|
| FREE | 1 | 1 | 5Gi | 512Mi | 0.5 | No |
| STANDARD | 5 | 3 | 50Gi | 4Gi | 2 | Yes |
| ENTERPRISE | unlimited | 5 | 500Gi | 16Gi | 4 | Yes |

Tier enforcement is bypassed entirely in self-hosted mode.

## Testing

- **Go**: 16 internal packages, 114 test files, all pass with `-race`. Run cmd: `go test ./internal/... -short -race -count=1` (~80s)
- **Postgres integration**: ~32 tests via testcontainers-go (`-tags=integration`); includes Docker restore E2E + Docker PITR E2E (target_name + target_xid + target_time variants) + pgdog notifier integration
- **K8s integration**: ~45 tests including k3s in Docker (`-tags=integration`)
- **R2 / S3 integration**: 2 tests gated on `R2_ACCESS_KEY_ID` (uploader-only + full pipeline). LocalStack covers the structural path; real R2 catches TLS-SAN / multipart quirks LocalStack doesn't reproduce.
- **Resend integration**: 1 live test gated on `RESEND_API_KEY`
- **Sister-repo contracts**: `internal/service/contract_test.go` pins ProjectID format, vault path shape, NATS CDC subject, JWT vault paths, PgDog reload subject. Runs in default `go test`.
- **Playwright E2E**: 27 spec files, 131 passing + 4 skipped (gated on `REAL_E2E=1` or `STUDIO_LIVE=1`); covers vault setup wizard, BYOC flow, deployment modes, edge functions, realtime, advisors, schema CRUD, backup history + restore (`backups.spec.ts`), live studio-vs-data-plane (`studio-live-data.spec.ts`)
- **Frontend unit**: Vitest suite for hooks and components (≥80% coverage)
- All critical paths use `MockClient` (fake K8s) and `fakeVault` for hermetic tests

## Key Files

- `cmd/server/main.go` — router + dependency wiring + store/vault/provisioner switch
- `internal/config/config.go` — env var parsing, `IsCloud()`
- `internal/provisioner/postgresql.go` — CNPG 9-stage pipeline
- `internal/provisioner/docker.go` — Docker SDK provisioner
- `internal/service/provisioning.go` — orchestration + vault cleanup on deprovision + BYOC
- `internal/service/pgdog_notifier.go` — NATS publish + config table CRUD
- `internal/service/backup.go` — adapter dispatch shim (resolveAdapter → K8s | Docker)
- `internal/service/backup_adapter.go` — `BackupAdapter` interface + `BackupRef` + `ErrUnsupportedBackupMode`
- `internal/service/backup_adapter_k8s.go` — CNPG flow (writes JSON-on-disk records)
- `internal/service/backup_adapter_docker.go` — Phase 1 MVP: pg_basebackup → S3 multipart, records in DB
- `internal/service/backup_runner_walg.go` — Phase 2: WAL-G runner (sidecar exec)
- `internal/service/backup_uploader.go` — aws-sdk-go-v2 multipart Upload/Download/Delete/List
- `internal/service/backup_scheduler.go` — robfig/cron + `LeaderLock` (Postgres advisory or `AlwaysLeader`)
- `internal/service/backup_restore_orchestrator.go` — async multi-step restore with `restore_jobs` rows
- `internal/service/wal_lag.go` — Docker-only `WalLagAdvertiser` interface
- `internal/email/resend_sender.go` — Resend SDK adapter (Phase 1 swap from SES)
- `internal/storage/postgres/store.go` — Postgres store (cloud)
- `internal/storage/sqlite/sqlite.go` — SQLite store (self-hosted)
- `internal/storage/platform_store.go` — combined store interface
- `internal/handler/org.go` — org management API
- `internal/handler/auth.go` — register, login, token management (argon2id)
- `internal/handler/function.go` — edge function CRUD + invoke + secrets + log SSE
- `internal/handler/realtime.go` — publication membership API
- `internal/handler/byoc.go` — BYOC registration
- `internal/handler/setup.go` — vault init wizard + first-admin creation
- `internal/auth/password.go` — argon2id hashing
- `internal/auth/org_rbac.go` — org + project permission maps
- `internal/edgefn/runtime_client.go` — Deno HTTP client + esbuild bundling
- `internal/k8s/crd_builder.go` — CNPG CRD generation
- `internal/k8s/helm.go` — Helm SDK for watcher + Deno pod deployment
- `internal/middleware/tenant.go` — TenantContext middleware (resolves project + org)
- `pkg/vault/vault.go` — Shamir + AES-256-GCM on any `VaultStore` (`store_postgres.go`; `store_memory.go` for tests)
- `internal/vaultclient/client.go` — VaultClient interface, HTTP implementation

## Helm Charts

- `charts/platform-base/` — CNPG platform-db cluster (3 instances, S3 backup)
- `charts/platform-aio/` — all-in-one bundle (CNPG + provisioning + deps)
- `charts/provisioning/` — provisioning server (ingress, RBAC, PDB)

## Configuration

Environment variables:

**Core**
- `PORT` — server port (default: 24005)
- `STORAGE_PATH` — data directory (default: ../provisioning-data)
- `LOG_LEVEL` — log level (default: debug)
- `CORS_ORIGINS` — comma-separated allowed origins (required, default: https://app.excalibase.io)
- `DEPLOYMENT_MODE` — `selfhosted` (default) or `cloud`
- `PUBLIC_BASE_URL` — public base URL for SDK snippets + function invoke

**Storage**
- `DB_PATH` — SQLite path (self-hosted only, default: ../provisioning-data/excalibase.db)
- `PLATFORM_DB_URL` — Postgres connection string (cloud mode required)

**Vault**
- `VAULT_URL` — remote vault HTTP URL (skips in-process vault if set)
- `VAULT_PAT` — PAT for remote vault auth

**Kubernetes**
- `KUBECONFIG_PATH` — explicit kubeconfig file
- `KUBE_API_URL` — remote K8s API URL (out-of-cluster deployments)
- `KUBE_BEARER_TOKEN` — ServiceAccount token for remote API
- `KUBE_CA_CERT` — PEM-encoded CA cert
- `KUBE_INSECURE_SKIP_VERIFY` — `true` to skip TLS verify (dev only)
- `WATCHER_CHART_PATH` — Helm chart path for CDC watcher (default: /charts/excalibase-watcher)

**Docker provisioner** (production peer of K8s — operational simplicity at the cost of weaker isolation; daemon access is root-equivalent)
- `PROVISIONER_MODE` — `k8s` (default) or `docker`
- `DOCKER_HOST` — Docker daemon URI. Local socket (`unix:///var/run/docker.sock`) or remote daemon over TLS (`tcp://host:2376`)
- `DOCKER_CERT_PATH` — TLS cert directory (`ca.pem`, `cert.pem`, `key.pem`) for remote daemon
- `DOCKER_TLS_VERIFY` — non-empty enables TLS verify (required for remote daemons)

**Edge Functions**
- `DENO_RUNTIME_URL` — Deno runtime HTTP endpoint
- `DENO_RUNTIME_SECRET` — shared secret for Deno auth
- `DENO_NAMESPACE` — K8s namespace for Deno pods (default: serverless)
- `DENO_RUNTIME_IMAGE` — image tag for per-project Deno pods

**Pooler / CDC**
- `NATS_URL` — NATS server for PgDog reload + CDC fan-out
- `REALTIME_PUBLICATION_NAME` — publication name (default: `cdc_watcher_pub`)

**Email**
- `EMAIL_PROVIDER` — `ses` (default, back-compat), `resend`, or `noop`. `noop` short-circuits to 503 so dev/CI runs without provider creds.
- `EMAIL_FROM_ADDRESS` / `EMAIL_FROM_NAME` — provider-agnostic defaults; provider-specific vars (`SES_FROM_ADDRESS`, `RESEND_FROM_ADDRESS`) override when set
- `EMAIL_PRODUCT_NAME` — string used in templates (verification + reset emails)
- `EMAIL_REPLY_TO` — optional Reply-To header
- SES path: `SES_ACCESS_KEY_ID`, `SES_SECRET_ACCESS_KEY`, `SES_REGION` (default `us-east-1`), `SES_FROM_ADDRESS`, `SES_FROM_NAME`, `SES_CONFIGURATION_SET` (optional, for bounce/complaint topic)
- Resend path: `RESEND_API_KEY`, `RESEND_FROM_ADDRESS`, `RESEND_FROM_NAME`, `RESEND_RATE_PER_SEC` (default 2 — free-tier limit)

**Backup defaults** (S3/R2 creds inherited by every new project unless `req.Backup.S3` is set explicitly; vault `backup/s3` takes priority over these envs)
- `BACKUP_DEFAULT_ACCESS_KEY_ID` / falls back to `R2_ACCESS_KEY_ID`
- `BACKUP_DEFAULT_SECRET_ACCESS_KEY` / falls back to `R2_SECRET_ACCESS_KEY`
- `BACKUP_DEFAULT_ENDPOINT` / falls back to `R2_ENDPOINT`
- `BACKUP_DEFAULT_BUCKET` (default `excalibase-backups`)
- `BACKUP_DEFAULT_REGION` (default `auto` for R2)
- `BACKUP_S3_PATH_STYLE` — set to `0` to disable path-style addressing (only flip for real AWS S3 buckets — R2/MinIO/LocalStack all need it on, the default)
- K8s restore reads the same config via `ProvisioningService.BackupStorage()` (env defaults, then vault `backup/s3`). No restore-side defaults, no localstack fallback — unconfigured storage makes restore fail with `ErrBackupStorageNotConfigured`.
