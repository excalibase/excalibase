# Docker Mode Backup / Restore / PITR — Plan

> **Status: Implemented (May 2026).** This doc is the original design
> rationale (tool decision, adapter shape, three-phase rollout). For
> current operator usage: **OPERATOR.md §6**. For where things live in
> the tree: **CLAUDE.md "Key Files"**. For the implementation diary
> (pre-flight surprises + risk decisions actually pinned):
> **DOCKER_BACKUP_IMPL.md**.

## Status quo (at time of writing — May 2026, pre-implementation)
- K8s mode: full feature parity via CNPG `ScheduledBackup` + Barman → R2. Tested.
- Docker mode: `ConfigureBackup` is a `return nil` stub. `BackupService.NewBackupService(...)` only takes a `*k8s.KubeClient`. Calling `POST /api/projects/{id}/backup/trigger` against a Docker-mode project silently no-ops.
- BYOC: explicitly out of scope (operator brings their own backups).

## Goal
Reach feature parity for Docker-mode PostgreSQL: scheduled backups, manual snapshots, listing, restore, PITR. Land it on an interface that scales to MySQL/MongoDB without a second rewrite.

## Tool decision: WAL-G

Three real candidates:

| | Barman | pgBackRest | **WAL-G** |
|---|---|---|---|
| Multi-engine (PG + MySQL + Mongo) | no | no | **yes** |
| Sidecar-friendly | needs separate server (or `barman-cloud` lite) | yes | yes |
| Object-store native (S3/R2/GCS) | via `barman-cloud-*` | yes | **first-class** |
| Language | Python | C + Perl | **Go (matches platform)** |
| PITR | yes | yes | yes |
| Maturity | very high (CNPG default) | very high (mission-critical PG) | high (Yandex prod, Citus prod) |

WAL-G wins on the multi-engine constraint. PG today, MySQL later (xtrabackup backend), Mongo after that (oplog backend). Same binary, same sidecar pattern, same R2 bucket.

K8s mode keeps CNPG+Barman; Docker mode uses WAL-G; the studio + API surface is identical because of the adapter pattern below.

## Architecture

```
┌─────────────────────── Per-project Docker network ──────────────────────┐
│                                                                          │
│   ┌─────────────┐   shared volumes      ┌───────────────────┐           │
│   │ postgres    │ ←── pgdata ─────→     │ walg-sidecar      │           │
│   │ container   │ ←── wal_archive ─────→│ (cron + wal-g)    │ ──→ R2   │
│   └─────────────┘                       └───────────────────┘           │
│         ↑                                                                │
│   archive_command = 'wal-g wal-push %p'                                 │
└──────────────────────────────────────────────────────────────────────────┘
```

Two volumes per project:
1. `pgdata-{projectId}` — Postgres data dir (already exists)
2. `walarchive-{projectId}` — WAL segments staged for upload

Sidecar lifecycle:
- Provisioned alongside the Postgres container by `DockerPostgreSQLProvisioner` (Phase 2)
- Reads WAL-G config from env (`WALG_S3_PREFIX`, `AWS_*` from vault path `projects/{org}/{project}/backup/s3`)
- Cron entry: `wal-g backup-push /var/lib/postgresql/data` at the configured cadence
- Each WAL segment is pushed by Postgres `archive_command` (synchronous)

## Adapter interface (Phase 1)

```go
// internal/service/backup_adapter.go

type BackupAdapter interface {
    Configure(ctx context.Context, inst *domain.DatabaseInstance, schedule string, retention int) error
    TriggerManual(ctx context.Context, inst *domain.DatabaseInstance) (BackupRef, error)
    List(ctx context.Context, inst *domain.DatabaseInstance) ([]BackupRef, error)
    Restore(ctx context.Context, inst *domain.DatabaseInstance, req domain.RestoreRequest) (*domain.ProvisioningResponse, error)
}

type BackupRef struct {
    ID         string
    StartedAt  time.Time
    FinishedAt *time.Time
    SizeBytes  int64
    Status     string  // "running" | "completed" | "failed"
    Method     string  // "pg_basebackup" | "wal-g" | "cnpg-barman"
}
```

`BackupService` becomes a thin router:

```go
type BackupService struct {
    store    storage.InstanceStore
    adapters map[domain.DeploymentMode]BackupAdapter  // K8s, Docker
}

func (s *BackupService) TriggerManualBackup(ctx context.Context, projectID string) (BackupRef, error) {
    inst, err := s.store.FindByProjectID(projectID)
    if err != nil { return BackupRef{}, err }
    a, ok := s.adapters[inst.DeploymentMode]
    if !ok { return BackupRef{}, fmt.Errorf("backup not supported for mode %s", inst.DeploymentMode) }
    return a.TriggerManual(ctx, inst)
}
```

Existing handlers + routes (`POST /backup/trigger`, `GET /backup/list`, `POST /restore`) keep their signatures. The studio doesn't change.

## Phase 1 — Adapter scaffold + Docker MVP (2-3 days)

**Goal:** make the gap explicit and ship a minimum-viable Docker backup using `pg_basebackup` → tar.gz → R2 upload. No WAL streaming, no PITR yet — just snapshots.

### Tasks

1. **Define `BackupAdapter` interface** in `internal/service/backup_adapter.go`
2. **Refactor existing `BackupService`** into a router with adapter map
3. **Implement `K8sBackupAdapter`** wrapping the existing CNPG logic verbatim
4. **Implement `DockerBackupAdapter`** MVP:
   - `Configure`: persist schedule + retention to instance metadata; register an in-process cron job
   - `TriggerManual`: `docker exec <pg-container> pg_basebackup -Ft -z -X fetch -D - | <stream to R2 via aws-go-sdk multipart>`
   - `List`: enumerate R2 keys under `backups/{projectId}/manual/*` and `backups/{projectId}/scheduled/*`
   - `Restore`: stop old container, fetch tar.gz, untar to fresh data dir, start new container, route via PgDog
5. **Wire** the adapter map in `cmd/server/main.go` based on `PROVISIONER_MODE`

### Tests (TDD red → green → refactor)

| Layer | Suite | Coverage target |
|---|---|---|
| Unit | `backup_adapter_test.go` — fake adapter, exercise router dispatch | dispatch logic 100% |
| Unit | `backup_adapter_k8s_test.go` — uses `MockClient` from `internal/k8s` | wraps existing tests; ensure no behavioral regression |
| Unit | `backup_adapter_docker_test.go` — fake docker client + fake S3 (using `httptest.Server`) | interface contract per method |
| Integration | `backup_adapter_docker_integration_test.go` (`//go:build integration`) — testcontainers Postgres + LocalStack S3 | real `pg_basebackup`, real S3 `multipart`, real restore |
| E2E | `frontend/e2e/backup-docker.spec.ts` — Playwright: provision Docker project → trigger backup → list shows it → restore creates new project | full flow validation |

### Acceptance gates (must pass to ship Phase 1)
- `go vet ./... && go vet -tags=integration ./...` clean
- `go test ./internal/... -short -race` clean
- `go test -tags=integration ./internal/service/` clean (the new Docker adapter tests)
- `npx playwright test backup-docker.spec.ts` passes
- Snyk Code: 0 findings
- Sonar: 0 findings (with the existing `scripts/sonar-local.sh` baseline)
- Conventional commit: `feat(backup): docker-mode backup MVP via pg_basebackup`

## Phase 2 — WAL-G sidecar + continuous archiving + PITR (4-5 days)

**Goal:** swap the MVP `pg_basebackup` flow for proper WAL streaming with PITR.

### New components

1. **`charts/walg-sidecar` Dockerfile** — pinned base (`alpine:3.20` + `wal-g/wal-g` v3.0.x), wraps the binary with a small entrypoint that:
   - Reads WAL-G config from env
   - Runs cron daemon for `wal-g backup-push` schedule
   - Tails postgres logs for archive failures and writes them to the platform's audit channel
2. **Image publish** — GHCR job mirroring how the Go server publishes (`ghcr.io/excalibase/walg-sidecar:0.1.0`)
3. **`DockerPostgreSQLProvisioner` updates**:
   - On provision, create the `walarchive-{projectId}` volume
   - Set `archive_mode=on`, `archive_command='wal-g wal-push %p'` in postgres conf via `ALTER SYSTEM` after init, then `SELECT pg_reload_conf()`
   - Start the sidecar container in the same Docker network with both volumes mounted
   - On deprovision, stop + remove the sidecar before the postgres container
4. **`DockerBackupAdapter` upgrades**:
   - `Configure`: rewrite the cron in the sidecar (via `docker exec ... crontab -`)
   - `TriggerManual`: `docker exec walg-sidecar wal-g backup-push /var/lib/postgresql/data` (replaces the pg_basebackup MVP)
   - `List`: `docker exec walg-sidecar wal-g backup-list --json`
   - `Restore` PITR: provision a new container with `restore_command = 'wal-g wal-fetch %f %p'` and `recovery_target_time = '<targetTime>'`

### Tests
- Unit: sidecar config builder, cron-spec normaliser
- Integration: testcontainers spins postgres + walg-sidecar + LocalStack S3, push WAL segments, validate they appear in S3, validate `wal-g backup-list` reflects manual + scheduled backups
- Integration: PITR — write rows, take backup, write more rows, restore to a target time *between* the two writes, assert only the first set of rows is present
- E2E: Playwright "Restore Point In Time" UI flow (already exists for K8s); add Docker case

### Acceptance gates
- All Phase 1 gates plus:
- WAL archiving lag metric exposed at `/api/projects/{id}/backup/wal-lag`
- Sidecar restart resilience: kill sidecar container, archive_command queues fail, sidecar comes back, queue drains
- Documented in `OPERATOR.md` §10: minimum disk space for `walarchive` volume, S3 retention reasoning, restore RTO

## Phase 3 — Restore-into-new-project orchestration parity (2-3 days)

**Goal:** Docker mode restore produces a *new* project (preserving the original), matching K8s behavior.

### Tasks
1. `RestoreFromBackup` in `DockerBackupAdapter`:
   - Allocate a new `projectId`
   - Provision a fresh postgres container with WAL-G bootstrap (`wal-g backup-fetch LATEST /var/lib/postgresql/data` before postgres start)
   - Apply `recovery.signal` + `recovery_target_time` if PITR
   - Start postgres in recovery mode, wait for `pg_isready`
   - Issue `pg_promote()` once recovery completes
   - Register the new instance in `BackupService` storage
   - Update PgDog routing to expose the new project
2. Studio UI: existing form already supports "New project name" field for K8s; reuse for Docker
3. Cleanup of failed restores (rollback if any step fails)

### Tests
- Unit: bootstrap-from-backup container config builder
- Integration: full restore → verify new container has data at the target time, original container untouched
- E2E: Playwright "Restore to new project" flow against a Docker-mode source project

### Acceptance gates
- Restore RTO < 5 minutes for a 100MB DB (measured in CI)
- Original project unaffected (verified by post-restore SELECT)
- PgDog routes the new project on the next config reload (NATS-published, < 5s)

## Out of scope for this plan
- MySQL backup adapter (Phase 4, lands when MySQL provisioner does)
- MongoDB backup adapter (Phase 5)
- Cross-region S3 replication (operator's call, configured at the bucket level)
- Encrypted-at-rest backups (server-side encryption is enabled in R2 by default; client-side via `WALG_LIBSODIUM_KEY` is opt-in via env)

## Risks + mitigations

| Risk | Mitigation |
|---|---|
| WAL-G binary CVEs | Pinned version in sidecar image; Snyk Container scan in CI |
| `archive_command` failures stall the WAL stream | Liveness probe on sidecar; alert via existing `internal/service/alerting.go` if archive_command exit code > 0 |
| Restore data loss from misconfigured PITR target | Validate target time ≥ earliest WAL retention before allowing restore; surface in API error |
| Two backup tools in one platform (CNPG-Barman + WAL-G) increases ops surface | Adapter interface keeps the platform code uniform; ops docs split per mode in OPERATOR.md |
| LocalStack drift from real S3 | All Phase 1+2 integration tests also have a `S3_BACKEND=r2` smoke test against a real R2 bucket gated by `INTEGRATION_R2=1` env (CI only, with secret) |

## Open questions
1. Should the K8s side also migrate to WAL-G (via CNPG plugin) to reduce tool surface? Decision deferred — only worth it if CNPG's Barman ever becomes a maintenance burden. For now: dual-stack is fine because the adapter abstracts it.
2. WAL retention default — 7 days? 14? Match the tier? Recommend tier-based: FREE 0d (no retention), STANDARD 7d, ENTERPRISE 30d. Document in `internal/config/tiers.go`.
3. Backup encryption — make `WALG_LIBSODIUM_KEY` mandatory or opt-in? Recommend opt-in; document the trade-off in OPERATOR.md.
