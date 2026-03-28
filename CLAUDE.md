# CLAUDE.md

## Project Overview

Excalibase Provisioning — Kubernetes-native database provisioning platform built with Go + client-go. Orchestrates CloudNativePG operator to provide unified REST API for PostgreSQL provisioning, backup/restore, and lifecycle management.

## Build & Run

```bash
# Build Go server
cd server-go && go build -o excalibase-server ./cmd/server/

# Run server (from server-go/)
PORT=24005 STORAGE_PATH=../provisioning-data ./excalibase-server

# Run tests (fast, unit only)
go test ./internal/... -race -cover

# Run all tests including k3s integration
go test ./internal/... -race -cover -timeout 7m

# Frontend
cd frontend && npm install && npm run dev
```

## Architecture

### Go Backend (server-go/)

Clean architecture with strict layer separation:

```
Handler (HTTP) → Service (business logic) → Provisioner (strategy) → K8s Client (client-go)
                                           → Storage (filesystem + AES encryption)
```

**Key principle**: All K8s operations use native `client-go` — no kubectl shell-outs.

### Project Structure

```
server-go/
├── cmd/server/main.go           # Entry point, router wiring
├── internal/
│   ├── config/                   # Tier config, app config
│   ├── domain/                   # Entities, DTOs, enums
│   ├── handler/                  # HTTP handlers (chi router)
│   ├── service/                  # Business logic
│   ├── provisioner/              # Strategy pattern (PostgreSQL, MySQL, MongoDB)
│   ├── k8s/                      # client-go wrapper, CRD builders, mock
│   ├── storage/                  # FileSystem stores (JSON + AES-256-GCM)
│   ├── security/                 # AES encryption
│   └── middleware/               # CORS
```

### Strategy Pattern for Database Provisioning

`DatabaseProvisioner` interface with implementations per database type:
- `PostgreSQLProvisioner` — CloudNativePG operator (fully functional)
- MySQL/MongoDB — planned

`Factory` selects the provisioner based on `DatabaseType`.

### 8-Stage Provisioning Pipeline

1. VALIDATING → 2. NAMESPACE_CREATION → 3. CRD_DEPLOYMENT → 4. WAITING_FOR_READY
5. CREDENTIAL_GENERATION → 6. BACKUP_CONFIGURATION → 7. METRICS_SETUP → 8. COMPLETED

### Kubernetes Integration

Uses `KubeClient` interface for testability:
- `Client` — real client-go implementation
- `MockClient` — in-memory test double

CRDs built as `unstructured.Unstructured` objects via `BuildPostgreSQLCluster()`.

### Storage

FileSystem-based with AES-256-GCM encryption:
- `provisioning-data/projects/{id}/metadata.json` — instance state
- `provisioning-data/projects/{id}/credentials.aes256` — encrypted credentials
- `provisioning-data/.encryption-key` — auto-generated AES key

### Tier Configuration

| Tier | Instances | Storage | Memory | CPU | PodMonitor |
|------|-----------|---------|--------|-----|------------|
| FREE | 1 | 5Gi | 512Mi | 0.5 | No |
| STANDARD | 3 | 50Gi | 4Gi | 2 | Yes |
| ENTERPRISE | 5 | 500Gi | 16Gi | 4 | Yes |

## Testing

- 207 tests, 85.8% coverage
- Unit tests with `MockClient` (fake K8s)
- Integration tests with `testcontainers-go` k3s (real K8s in Docker)
- Race detection enabled (`-race`)
- CRD schema validation tests

## Key Files

- `cmd/server/main.go` — router + dependency wiring
- `internal/provisioner/postgresql.go` — 8-stage pipeline
- `internal/k8s/crd_builder.go` — CNPG CRD generation
- `internal/k8s/client.go` — client-go wrapper
- `internal/k8s/mock.go` — test mock
- `internal/service/metrics.go` — CNPG metrics + metrics-server
- `internal/service/provisioning.go` — orchestration
- `internal/storage/filesystem.go` — JSON + AES storage

## Configuration

Environment variables:
- `PORT` — server port (default: 24005)
- `STORAGE_PATH` — data directory (default: ../provisioning-data)
- `LOG_LEVEL` — log level (default: debug)
