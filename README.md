# Excalibase Provisioning

Kubernetes-native database provisioning platform. Built with Go + client-go.

## What It Does

One API call provisions a production PostgreSQL cluster with backup, monitoring, and performance insights:

```bash
curl -X POST http://localhost:24005/api/provision \
  -d '{"projectName":"my-db","orgId":"myorg","databaseType":"POSTGRESQL","tier":"STANDARD"}'
```

- **8-stage provisioning pipeline** — namespace, CRD deployment, pod readiness, credential extraction, backup config, metrics setup
- **3 tiers** — FREE (1 pod), STANDARD (3 pods + HA), ENTERPRISE (5 pods)
- **Real metrics** — per-pod CPU/memory from metrics-server + CNPG database metrics from port 9187
- **Backup & PITR** — automated WAL archiving + scheduled backups + point-in-time recovery
- **Performance insights** — pg_stat_statements: top queries, cache hit ratio, wait events

## Architecture

```
server-go/       Go backend (chi router, client-go, 27MB binary)
frontend/        React dashboard (Vite, Tailwind, Recharts)
k8s/             Kubernetes manifests
```

The Go server uses `client-go` natively — no kubectl shell-outs, no Fabric8 serialization issues. CRDs are built as `unstructured.Unstructured` objects and applied via the dynamic client.

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

### 4. Provision a database

```bash
curl -X POST http://localhost:24005/api/provision \
  -H "Content-Type: application/json" \
  -d '{
    "projectName": "duke-database",
    "orgId": "exca",
    "databaseType": "POSTGRESQL",
    "tier": "STANDARD",
    "backup": {"enabled": true, "schedule": "0 2 * * *", "retention": 30},
    "parameters": {"shared_preload_libraries": "pg_stat_statements"}
  }'
```

## API Endpoints

| Endpoint | Description |
|----------|-------------|
| `GET /api/provision` | List all instances |
| `POST /api/provision` | Provision new database |
| `GET /api/provision/{id}` | Instance status |
| `DELETE /api/provision/{id}` | Delete instance |
| `GET /api/provision/{id}/credentials` | Connection details |
| `GET /api/provision/{id}/metrics/current` | Real-time metrics + per-pod CPU/memory |
| `POST /api/provision/{id}/backup/trigger` | Manual backup |
| `POST /api/provision/{id}/backup/restore` | Restore / PITR |
| `GET /api/provision/{id}/performance/summary` | pg_stat_statements summary |
| `GET /api/provision/{id}/performance/top-queries` | Top queries by time |
| `POST /api/provision/{id}/migrations` | Apply SQL migration |
| `POST /api/provision/{id}/snapshot/export` | pg_dump export |
| `GET /api/setup/status` | Operator installation status |

See full API reference in the [docs](https://excalibase.io/docs/provisioning/api-reference).

## Testing

```bash
cd server-go

# Unit tests (fast, <2s)
go test ./internal/... -race -cover

# Full suite including k3s integration (~2min, needs Docker)
go test ./internal/... -race -cover -timeout 7m
```

207 tests, 85.8% coverage, zero race conditions. Integration tests spin up a real k3s cluster in Docker via testcontainers-go.

## Tech Stack

- **Backend**: Go 1.24, chi router, client-go, AES-256-GCM encryption
- **Frontend**: React 19, Vite, Tailwind CSS, Recharts, TanStack Query
- **K8s Operators**: CloudNativePG (PostgreSQL), Vitess (MySQL planned), MongoDB Community (planned)
- **Metrics**: Kubernetes metrics-server (CPU/memory), CNPG Prometheus exporter (connections, WAL, DB size)
- **Backup**: barmanObjectStore → S3 (LocalStack for dev, AWS for production)
- **Testing**: testify, httptest, client-go fake, testcontainers-go k3s

## License

Apache 2.0
