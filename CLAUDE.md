# CLAUDE.md

## Project Overview

Excalibase Provisioning — Kubernetes-native database provisioning platform built with Go + client-go. Orchestrates CloudNativePG operator to provide unified REST API for PostgreSQL provisioning, backup/restore, and lifecycle management. Includes org/project RBAC, PgDog connection pooler integration, and a React frontend.

## Build & Run

```bash
# Build Go server
cd server-go && go build -o excalibase-server ./cmd/server/

# Run server (from server-go/) — SQLite (local dev)
PORT=24005 STORAGE_PATH=../provisioning-data ./excalibase-server

# Run server with Postgres platform DB
PLATFORM_DB_URL=postgres://platform:pass@localhost:5432/platform ./excalibase-server

# Run tests (fast, unit only — excludes k8s integration)
go test $(go list ./internal/... | grep -v /k8s) -race -cover

# Run all tests including k3s + Postgres integration
go test -tags=integration ./internal/... -race -cover -timeout 7m

# Frontend
cd frontend && npm install && npm run dev

# Playwright E2E
cd frontend && npx playwright test
```

## Architecture

### Go Backend (server-go/)

Clean architecture with strict layer separation:

```
Handler (HTTP) → Service (business logic) → Provisioner (strategy) → K8s Client (client-go)
                                           → Storage (SQLite or Postgres)
                                           → PgDog Notifier (NATS)
```

**Key principle**: All K8s operations use native `client-go` — no kubectl shell-outs.

### Project Structure

```
server-go/
├── cmd/server/main.go           # Entry point, router wiring
├── internal/
│   ├── auth/                     # JWT, RBAC (platform/org/project levels)
│   ├── config/                   # Tier config, app config
│   ├── domain/                   # Entities, DTOs, enums
│   ├── handler/                  # HTTP handlers (chi router)
│   ├── service/                  # Business logic + PgDog notifier
│   ├── provisioner/              # Strategy pattern (PostgreSQL, MySQL, MongoDB)
│   ├── k8s/                      # client-go wrapper, CRD builders, Helm SDK, mock
│   ├── storage/
│   │   ├── sqlite/               # SQLite store (local dev)
│   │   └── postgres/             # Postgres store (production via CNPG)
│   ├── security/                 # AES encryption
│   ├── middleware/               # CORS, security headers
│   └── vault/                    # Shamir vault (bbolt)
```

### Strategy Pattern for Database Provisioning

`DatabaseProvisioner` interface with implementations per database type:
- `PostgreSQLProvisioner` — CloudNativePG operator (fully functional)
- MySQL/MongoDB — planned

`Factory` selects the provisioner based on `DatabaseType`.

### 9-Stage Provisioning Pipeline

1. VALIDATING → 2. NAMESPACE_CREATION → 3. CRD_DEPLOYMENT → 4. WAITING_FOR_READY
5. CREDENTIAL_GENERATION → 6. BACKUP_CONFIGURATION → 7. METRICS_SETUP
8. WATCHER_DEPLOYMENT → 9. COMPLETED

After stage 5, the provisioner also registers the cluster with PgDog (INSERT into pgdog_databases/pgdog_users + NATS publish).

### Kubernetes Integration

Uses `KubeClient` interface for testability:
- `Client` — real client-go implementation
- `MockClient` — in-memory test double

CRDs built as `unstructured.Unstructured` objects via `BuildPostgreSQLCluster()`.
Helm SDK for deploying per-project watchers.

### Storage

Dual-backend storage — switch via `PLATFORM_DB_URL` env var:
- **SQLite** (default, local dev): `STORAGE_PATH/excalibase.db`
- **PostgreSQL** (production): CNPG platform-db cluster, auto-migrates on startup

Both implement the same `PlatformStore` interface (InstanceStore + UserStore + TokenStore + OrgStore).

### Connection Pooler (PgDog)

Centralized PgDog pooler routes client connections to per-project CNPG clusters:
- Config stored in `pgdog_databases` / `pgdog_users` tables in platform-db
- NATS-triggered reload — provisioner publishes `pgdog.config.reload` after cluster creation
- Read/write splitting: SELECTs → replicas, writes → primary
- Fork repo: github.com/excalibase/pgdog

### RBAC

Three-level role system:
- **Platform**: platform_admin, platform_operator, platform_viewer, user
- **Org**: owner, admin, developer, viewer
- **Project**: admin, editor, viewer

### Tier Configuration

| Tier | MaxProjects | Instances | Storage | Memory | CPU | Backup |
|------|------------|-----------|---------|--------|-----|--------|
| FREE | 1 | 1 | 5Gi | 512Mi | 0.5 | No |
| STANDARD | 5 | 3 | 50Gi | 4Gi | 2 | Yes |
| ENTERPRISE | unlimited | 5 | 500Gi | 16Gi | 4 | Yes |

## Testing

- 14 Go packages pass with `-race`
- 25 Postgres integration tests (testcontainers)
- 97 Playwright E2E tests
- 45 k8s tests (including k3s integration)
- Unit tests with `MockClient` (fake K8s)
- Race detection enabled (`-race`)

## Key Files

- `cmd/server/main.go` — router + dependency wiring + store switch
- `internal/provisioner/postgresql.go` — 9-stage pipeline
- `internal/service/provisioning.go` — orchestration + PgDog registration
- `internal/service/pgdog_notifier.go` — NATS publish + config table CRUD
- `internal/storage/postgres/store.go` — Postgres store (production)
- `internal/storage/sqlite/sqlite.go` — SQLite store (dev)
- `internal/storage/platform_store.go` — combined store interface
- `internal/handler/org.go` — org management API (13 endpoints)
- `internal/handler/auth.go` — register, login, token management
- `internal/auth/org_rbac.go` — org + project permission maps
- `internal/k8s/crd_builder.go` — CNPG CRD generation
- `internal/k8s/helm.go` — Helm SDK for watcher deployment

## Helm Charts

- `charts/platform-base/` — CNPG platform-db cluster (3 instances, S3 backup)
- `charts/provisioning/` — provisioning server deployment (ingress, RBAC, PDB)

## Configuration

Environment variables:
- `PORT` — server port (default: 24005)
- `STORAGE_PATH` — data directory (default: ../provisioning-data)
- `LOG_LEVEL` — log level (default: debug)
- `DB_PATH` — SQLite path (default: ../provisioning-data/excalibase.db)
- `PLATFORM_DB_URL` — Postgres connection string (if set, uses Postgres instead of SQLite)
- `NATS_URL` — NATS server for PgDog reload signals (optional)
- `CORS_ORIGINS` — allowed origins (default: https://app.excalibase.io)
- `WATCHER_CHART_PATH` — Helm chart path for CDC watcher
