# Excalibase Provisioning

Kubernetes-native database provisioning platform. Built with Go + client-go.

## What It Does

One API call provisions a production PostgreSQL cluster with backup, monitoring, and performance insights:

```bash
curl -X POST http://localhost:24005/api/provision \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"projectName":"my-db","orgId":"myorg","databaseType":"POSTGRESQL","tier":"STANDARD"}'
```

- **9-stage provisioning pipeline** — namespace, CRD deployment, pod readiness, credential extraction, backup config, metrics setup, watcher deployment
- **3 tiers** — FREE (1 pod), STANDARD (3 pods + HA), ENTERPRISE (5 pods)
- **Real metrics** — per-pod CPU/memory from metrics-server + CNPG database metrics from port 9187
- **Backup & PITR** — automated WAL archiving + scheduled backups + point-in-time recovery
- **Performance insights** — pg_stat_statements: top queries, cache hit ratio, wait events
- **Schema management** — full SQL schema introspection, CRUD for tables/columns/roles/extensions/policies/functions/triggers/indexes
- **Vault** — Shamir secret sharing for credentials, AES-256-GCM encryption at rest

## Architecture

```
server-go/       Go backend (chi router, client-go)
├── cmd/server/    Entry point, router wiring, CLI tools
├── internal/
│   ├── handler/     HTTP handlers (auth, orgs, provisioning, schema, etc.)
│   ├── service/     Business logic (provisioning, metrics, PgDog notifier)
│   ├── provisioner/ Strategy pattern (PostgreSQL, MySQL planned)
│   ├── k8s/         client-go wrapper, Helm SDK, CRD builders, mock
│   ├── schema/      SQL introspection + parameterized query builder
│   ├── storage/
│   │   ├── sqlite/    SQLite store (local dev)
│   │   └── postgres/  Postgres store (production via CNPG)
│   ├── vault/       Shamir-based secret vault (bbolt + AES-256-GCM)
│   ├── edgefn/      Edge function store + Deno runtime client
│   ├── auth/        3-level RBAC (platform/org/project), JWT, bcrypt
│   ├── middleware/   CORS, security headers
│   └── security/    AES encryption for filesystem storage
frontend/        React 18 dashboard (Vite, Tailwind, TanStack)
charts/
├── platform-base/  CNPG platform-db cluster (3 instances, S3 backup)
└── provisioning/   Provisioning server Helm chart
deno-server/     Deno edge functions runtime (Web Workers, sandboxed)
```

The Go server uses `client-go` natively — no kubectl shell-outs. CRDs are built as `unstructured.Unstructured` objects and applied via the dynamic client.

## Quick Start

### Prerequisites

- Kubernetes cluster (minikube, kind, k3s, or Docker Desktop)
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

```bash
# Initialize vault (returns unseal shares)
curl -s -X POST http://localhost:24005/api/vault/init \
  -H "Content-Type: application/json" \
  -d '{"shares": 5, "threshold": 3}'

# Unseal (repeat with 3 different shares)
curl -s -X POST http://localhost:24005/api/vault/unseal \
  -H "Content-Type: application/json" \
  -d '{"share": "<share-hex>"}'

# Login (returns token)
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

All endpoints (except login, vault init/unseal/status) require authentication via `Authorization: Bearer <token>` header.

### Authentication

| Method | Endpoint | Auth | Description |
|--------|----------|------|-------------|
| POST | `/api/auth/register` | No | Public registration |
| POST | `/api/auth/login` | No | Login, returns bearer token |
| GET | `/api/auth/me` | Yes | Current user info |
| GET | `/api/auth/users` | Yes (admin) | List all users |
| POST | `/api/auth/users` | Yes (admin) | Create user |
| DELETE | `/api/auth/users/{id}` | Yes (admin) | Delete user |
| GET | `/api/auth/tokens` | Yes | List personal access tokens |
| POST | `/api/auth/tokens` | Yes | Create PAT |
| DELETE | `/api/auth/tokens/{hash}` | Yes | Revoke PAT |

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
| POST | `/api/provision` | Yes | Provision new database |
| POST | `/api/provision/estimate` | Yes | Cost estimate |
| GET | `/api/provision/{id}` | Yes | Instance status |
| DELETE | `/api/provision/{id}` | Yes | Delete instance |
| GET | `/api/provision/{id}/credentials` | Yes | Connection details |
| POST | `/api/provision/{id}/credentials/rotate` | Yes | Rotate credentials |
| GET | `/api/provision/{id}/logs` | Yes | Pod logs |
| GET | `/api/provision/{id}/metrics/current` | Yes | Real-time metrics |
| GET | `/api/provision/{id}/metrics/history` | Yes | Metrics history |
| POST | `/api/provision/{id}/backup/trigger` | Yes | Manual backup |
| GET | `/api/provision/{id}/backup/list` | Yes | List backups |
| POST | `/api/provision/{id}/backup/restore` | Yes | Restore / PITR |
| GET | `/api/provision/{id}/performance/summary` | Yes | pg_stat_statements summary |
| GET | `/api/provision/{id}/performance/top-queries` | Yes | Top queries by time |
| POST | `/api/provision/{id}/migrations` | Yes | Apply SQL migration |
| POST | `/api/provision/{id}/snapshot/export` | Yes | pg_dump export |

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

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `24005` | Server port |
| `STORAGE_PATH` | `../provisioning-data` | Data directory |
| `DB_PATH` | `../provisioning-data/excalibase.db` | SQLite database path |
| `PLATFORM_DB_URL` | | Postgres connection string (overrides SQLite) |
| `NATS_URL` | | NATS server for PgDog reload signals |
| `CORS_ORIGINS` | `https://app.excalibase.io` | Comma-separated allowed origins |
| `WATCHER_CHART_PATH` | `/charts/excalibase-watcher` | Helm chart for CDC watcher |
| `DENO_RUNTIME_URL` | `http://deno-runtime...` | Deno edge functions runtime URL |
| `DENO_RUNTIME_SECRET` | | Shared secret for Deno runtime auth |

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

14 Go packages, 25 Postgres integration tests, 45 k8s tests, 97 Playwright E2E tests. Integration tests use testcontainers-go (real PostgreSQL + k3s in Docker).

## Tech Stack

- **Backend**: Go 1.25, chi router, client-go, Helm SDK, bbolt vault
- **Storage**: SQLite (dev) / PostgreSQL via CNPG (production), auto-migrate on startup
- **Connection Pooler**: PgDog fork (Postgres-backed config + NATS reload)
- **Frontend**: React 18, Vite, Tailwind CSS, TanStack Query/Table, CodeMirror 6, cmdk
- **K8s Operators**: CloudNativePG (PostgreSQL), Vitess (MySQL planned), MongoDB Community (planned)
- **Metrics**: Kubernetes metrics-server (CPU/memory), CNPG Prometheus exporter
- **Backup**: barmanObjectStore → S3 (LocalStack for dev, AWS for production)
- **Testing**: httptest, testcontainers-go (k3s + PostgreSQL), Playwright

## License

Apache 2.0
