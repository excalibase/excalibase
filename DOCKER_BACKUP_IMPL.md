# Docker Mode Backup / Restore / PITR — Implementation Plan

> **Status: Implemented (May 2026).** Phases 0–3 landed in commits
> `b75c9a5` → `5fdf621` on `feat/security-hardening-studio`. This doc
> is now historical — it captures the plan, the pre-flight surprises,
> and the risk decisions that got pinned. For current operator-facing
> usage see **OPERATOR.md §6**; for code structure see **CLAUDE.md
> "Key Files"** (the `internal/service/backup_*` block).
>
> Drift notes vs. what shipped:
> - Phase 1B used `pg_basebackup` streaming via `docker exec` + stdcopy
>   demux (not the scratch-volume + uploader-container split this doc
>   recommended). The simpler path turned out to be testable and
>   robust enough; uploader-container approach deferred unless we hit
>   bandwidth limits in production.
> - Phase 1E Playwright spec (`frontend/e2e/backups.spec.ts`) shipped
>   mock-based, not against a Docker-mode dev stack. The UI-glue
>   surface was the value; mode-specific E2E is gated on real-stack
>   plumbing that doesn't yet exist.
> - Real-R2 E2E test (`backup_uploader_r2_test.go`,
>   `backup_adapter_docker_r2_test.go`) added gated on `R2_ACCESS_KEY_ID`
>   — verifies path-style URLs + R2 multipart quirks LocalStack can't.


> **The walg-sidecar image is gone (EXC-432, September 2026).** The chart
> directory and its publish workflow were deleted: nothing in the platform
> ever deployed that image — the backup path does not use it and restore
> runs the catalogue's Postgres image — so it published an artefact no
> code consumed. References to `charts/walg-sidecar` below are history.

---

Companion to `DOCKER_BACKUP_PLAN.md`. That doc is the *what*; this is
the *how*, ordered, with file paths, test names, gates, and the open
questions to pin before TDD starts.

## 0. Reality check vs. the plan

Three things in `DOCKER_BACKUP_PLAN.md` don't match the code today:

1. **`inst.DeploymentMode` is never written for `k8s` or `docker` provisions.** Only the BYOC path sets it (`internal/service/provisioning.go:195`). The dispatch table `adapters[inst.DeploymentMode]` will hit the zero-value (`""`) for every existing project. **Adapter dispatch is broken before it ships.** Phase-0 pre-flight, not Phase-1.
2. **`database_instances` table has no `deployment_mode` column.** Migration `000003_project_refactor.up.sql` adds project_name/current_step/etc. but never `deployment_mode`. The struct field has a JSON tag (`internal/domain/instance.go:46`) but the Postgres store never persists it. New migration required.
3. **`ConfigureBackup` lives on `provisioner.DatabaseProvisioner`** (`internal/provisioner/provisioner.go:19`) — it is *not* a separate concern today. The plan says "BackupService becomes a thin router" but doesn't say what happens to the existing provisioner method. Recommendation: leave `ConfigureBackup` on the provisioner for the *initial* schedule write at provision time, have the adapter call into it for in-life schedule changes. Don't rip it out — the rollback context (`stageBackup` in `postgresql.go:284`) depends on it.

Other confirmations:
- `BackupHandler` routes (`internal/handler/backup.go:20-24`) are stable: `POST /trigger`, `GET /list`, `POST /restore` mounted at `/backup` (`cmd/server/main.go:352`). Plan correct.
- `BackupRecordStore` interface exists in `internal/storage/store.go:47-51` and `backup_records` table exists, **but is currently unused** — `BackupService` writes JSON files via `s.saveBackupRecord` (`internal/service/backup.go:246`). Standardise on `BackupRecordStore` for Docker mode (JSON-on-disk doesn't survive container restarts cleanly and isn't multi-instance safe). **[POST-IMPLEMENTATION:** the split shipped exactly this way — Docker adapter persists to `BackupRecordStore` (DB-backed via `PlatformStore.BackupRecords()`), K8s adapter still writes JSON files to `{storagePath}/projects/{projectId}/backups/`. Operators viewing K8s vs Docker projects will see different audit trails — see CLAUDE.md "Key Files" for which adapter writes where.**]
- No cron library in `go.sum` today. Phase 1 will add `github.com/robfig/cron/v3` (actively maintained, tiny, no transitive deps; `gocron` pulls in too much).
- No Playwright spec for backups exists. `frontend/e2e/byoc.spec.ts` is the closest reference.
- `cfg.DeploymentMode` (`internal/config/config.go:25`, `"selfhosted"` vs `"cloud"`) is a different concept from `domain.DeploymentMode` (`"k8s"|"docker"|"byoc"`). Same name, different meaning. Don't conflate. Dispatch keys on `domain.DeploymentMode`.
- Provisioner mode comes from `cfg.ProvisionerMode` (`"k8s"` or `"docker"`), wired in `cmd/server/main.go:570-586`. Adapter map should be built from `cfg.ProvisionerMode` for the active provisioner, but **dispatch should still consult `inst.DeploymentMode`** for already-provisioned instances — a platform that has run in both modes will have mixed instances.

## 1. Plan items I'd push back on

- **`docker exec pg_basebackup -Ft -z -X fetch -D - | stream to R2`** (Phase 1) is fragile. `docker exec` stdout streaming through the Docker daemon is bandwidth-limited and the cancellation story on a partial upload is ugly (you can kill exec, but R2 multipart upload state needs explicit `AbortMultipartUpload` or you pay storage for orphaned parts indefinitely). **Push:** Phase 1 still uses `pg_basebackup`, but writes to a *temporary scratch volume* mounted on the postgres container, then a separate, short-lived "uploader" container reads from that volume and does the multipart upload via the standard `aws-sdk-go-v2` flow. Same code path you'll need anyway in Phase 2 for the WAL-G sidecar, so you don't throw it away.
- **In-process cron in the platform server.** The plan implies it. This is the *single biggest risk* — see Risk #1 below.
- **`recovery_target_time` PITR is the only restore variant.** Operationally the more common ask is "restore to *just before* the bad migration ran." That's `recovery_target_xid` or `recovery_target_lsn`. Phase 3 should accept a `RestoreTarget` union (`time | xid | lsn | name`), not just `targetTime`. Cheap to design now, expensive to retrofit.
- **`WALG_LIBSODIUM_KEY` opt-in.** Flip this. Backups in R2 are at rest in *Cloudflare's* keys. For ENTERPRISE tier the regulatory bar is "encrypted with keys not held by the storage provider" — that means client-side. Opt-out per project is fine; opt-in by default leaves enterprise customers paying for a feature they have to discover.

## 2. Phase 0 — Pre-flight (~half a day)

Structural fixes the plan assumes are already done. They aren't.

### Files to modify

| File | Change | Risk |
|---|---|---|
| `server-go/internal/storage/postgres/migrations/000007_deployment_mode.up.sql` (NEW) | `ALTER TABLE database_instances ADD COLUMN IF NOT EXISTS deployment_mode TEXT NOT NULL DEFAULT 'k8s';` plus matching `.down.sql` and parallel `sqlite/migrations/000007*` | low — additive, default backfills existing rows correctly |
| `server-go/internal/storage/postgres/pg_instances.go` (verify path) | Read/write the new column in Save / FindByProjectID | medium — touches every read path |
| `server-go/internal/storage/filesystem.go` | Same: persist `DeploymentMode` in JSON | low |
| `server-go/internal/service/provisioning.go:322` | In `prepareProvisioning`, set `inst.DeploymentMode` from a new constructor arg or from a service-level flag set in `cmd/server/main.go` from `cfg.ProvisionerMode` | medium — wrong default leaks across modes |
| `server-go/cmd/server/main.go:68` (`NewProvisioningService`) | Pass `domain.DeploymentMode` derived from `cfg.ProvisionerMode` | low |

### Test files to add

- `server-go/internal/storage/postgres/pg_test.go` — extend with `TestSaveAndFind_DeploymentMode_RoundTrips` and `TestFind_LegacyRow_DefaultsToK8s`
- `server-go/internal/service/provisioning_test.go` — `TestProvision_SetsDeploymentMode_K8s` and `TestProvision_SetsDeploymentMode_Docker`

### TDD red names

- `t.Run("save_round_trips_deployment_mode", ...)`
- `t.Run("legacy_row_defaults_to_k8s", ...)`
- `t.Run("provision_k8s_sets_mode_k8s", ...)`
- `t.Run("provision_docker_sets_mode_docker", ...)`

### Acceptance gate

- `go test ./internal/storage/... ./internal/service/... -race` clean
- Existing instances (loaded via `FindAll`) report `DeploymentMode == ModeK8s`
- New instances report the correct mode based on provisioner
- One forward + rollback dry-run of the migration on a copy of staging

## 3. Phase 1 — Adapter scaffold + Docker MVP (3-4 days)

### New files

| Path | Purpose |
|---|---|
| `server-go/internal/service/backup_adapter.go` | `BackupAdapter` interface, `BackupRef` struct, dispatch errors |
| `server-go/internal/service/backup_adapter_k8s.go` | `K8sBackupAdapter` — wraps existing CNPG logic verbatim |
| `server-go/internal/service/backup_adapter_docker.go` | `DockerBackupAdapter` — Phase 1 MVP using `pg_basebackup` + scratch volume + uploader container |
| `server-go/internal/service/backup_uploader.go` | S3/R2 multipart upload helper using `aws-sdk-go-v2` (factored so Phase 2 reuses) |
| `server-go/internal/service/backup_scheduler.go` | Cron wrapper around `robfig/cron/v3` with persistent schedule (DB-backed, see Risk #1) |
| `server-go/internal/provisioner/docker_volume.go` | Helper that owns `pgdata-{id}` and `walarchive-{id}` volume lifecycle (Phase 2 needs the second one — split now so Phase 2 doesn't grow the provisioner) |

### Existing files to modify

| File | Change | Risk |
|---|---|---|
| `internal/service/backup.go` | Refactor: keep `BackupService` struct but reduce body to dispatch only. Move CNPG specifics to `backup_adapter_k8s.go`. Add adapters map. | **HIGH** — every existing backup test must still pass without modification |
| `internal/service/backup.go` `NewBackupService` | New signature: `NewBackupService(store, adapters map[domain.DeploymentMode]BackupAdapter, storagePath string)`. Backwards-compat constructor `NewBackupServiceLegacy(store, k8sClient, storagePath)` builds the adapter map internally so old call sites compile. | medium — `cmd/server/main.go:226` must update |
| `cmd/server/main.go:226` | Build the adapter map: always include K8s adapter; include Docker adapter when `cfg.ProvisionerMode == "docker"` (and Docker client is non-nil). | low |
| `internal/handler/backup.go` | NO CHANGES. (verify the route signatures are unchanged after refactor — gate.) | low |
| `internal/storage/postgres/pg_*.go` | Add `BackupRecordStore` Postgres impl (currently only the interface exists) | medium |
| `internal/service/backup_test.go` | Existing tests must keep passing untouched. | high — regression gate |

### New test files

| Path | What it covers | Build tag |
|---|---|---|
| `internal/service/backup_adapter_test.go` | Router dispatch, error on unknown mode, error when adapter returns error | none (unit) |
| `internal/service/backup_adapter_k8s_test.go` | All previous CNPG behavior wrapped, using existing `k8s.MockClient` | none |
| `internal/service/backup_adapter_docker_test.go` | Fakes `provisioner.DockerClient` and the uploader. Drives Configure/Trigger/List/Restore. | none |
| `internal/service/backup_scheduler_test.go` | Schedule add/remove, persistence after restart (uses sqlite store), schedule fires correctly | none |
| `internal/service/backup_uploader_test.go` | Multipart upload happy path + abort on cancel; uses `httptest.Server` mocking S3 surface | none |
| `internal/service/backup_adapter_docker_integration_test.go` | testcontainers Postgres + LocalStack S3, real `pg_basebackup`, real multipart, real restore-into-fresh-container | `//go:build integration` |
| `frontend/e2e/backups.spec.ts` | Provision Docker project, trigger backup, list shows COMPLETED, restore creates new project, target project has source data | none — Playwright |

### TDD red test names (Phase 1)

`backup_adapter_test.go`:
- `t.Run("dispatches_k8s_to_k8s_adapter", ...)`
- `t.Run("dispatches_docker_to_docker_adapter", ...)`
- `t.Run("returns_error_for_unsupported_mode", ...)`
- `t.Run("returns_error_when_instance_not_found", ...)`
- `t.Run("propagates_adapter_error_unchanged", ...)`

`backup_adapter_docker_test.go`:
- `t.Run("trigger_manual_creates_basebackup_in_scratch_volume", ...)`
- `t.Run("trigger_manual_uploads_to_r2_under_correct_prefix", ...)` (`backups/{projectId}/manual/{timestamp}.tar.gz`)
- `t.Run("trigger_manual_aborts_multipart_on_cancel", ...)`
- `t.Run("trigger_manual_writes_backup_record_with_in_progress_then_completed", ...)`
- `t.Run("list_returns_records_from_store_not_filesystem", ...)` (deliberate divergence from CNPG path)
- `t.Run("list_filters_by_project_id_only_idor_safe", ...)` ← critical, see gap §7
- `t.Run("configure_persists_schedule_to_db_and_registers_cron", ...)`
- `t.Run("configure_replaces_existing_schedule_on_re_register", ...)`
- `t.Run("restore_blocks_when_target_time_predates_oldest_backup", ...)`
- `t.Run("restore_blocks_when_new_project_id_collides_with_existing", ...)`

`backup_scheduler_test.go`:
- `t.Run("schedule_persists_across_restart", ...)`
- `t.Run("schedule_skips_run_when_instance_deprovisioned", ...)`
- `t.Run("schedule_invalid_cron_returns_error_at_register", ...)`
- `t.Run("concurrent_runs_for_same_project_are_serialised", ...)`

`backups.spec.ts`:
- `test("Docker project: trigger backup → list shows it → restore creates new project")`
- `test("Docker project: backup list filtered by project (no IDOR across orgs)")`

### Acceptance gates (Phase 1 done when ALL pass)

- `go vet ./... && go vet -tags=integration ./...` clean
- `go test ./internal/... -short -race -count=1` clean
- `go test -tags=integration -run TestDockerBackupAdapter ./internal/service/` clean
- `npx playwright test backups.spec.ts` passes against a Docker-mode dev stack
- Existing CNPG tests pass *unchanged* — proof that the refactor didn't regress K8s mode
- Snyk Code: 0 new findings
- `gosec ./...` clean
- `BackupRecordStore` is the source of truth for backup history in Docker mode (verified by deleting the JSON-on-disk dir and confirming the API still returns the records)
- One end-to-end run on a real R2 bucket (`INTEGRATION_R2=1`) — gated, but actually run before merge
- Conventional commit: `feat(backup): docker-mode backup MVP via pg_basebackup`

## 4. Phase 2 — WAL-G sidecar + PITR (5-6 days)

### Pre-flight

The sidecar image *must be published to GHCR* before integration tests can pull it in CI. Half-day workflow setup (mirrors the existing Go server publish).

### New files

| Path | Purpose |
|---|---|
| `charts/walg-sidecar/Dockerfile` | Pinned `alpine:3.20` + `wal-g/wal-g` v3.0.x + tini + minimal cron (busybox crond) |
| `charts/walg-sidecar/entrypoint.sh` | Reads env, writes crontab, starts crond foreground; tails postgres archive logs to stderr |
| `charts/walg-sidecar/healthcheck.sh` | Exit 0 if `wal-g` binary works AND last archive_command succeeded within N minutes |
| `.github/workflows/walg-sidecar-publish.yml` | Build + push to `ghcr.io/excalibase/walg-sidecar:0.1.0` on tag `walg-sidecar-v*` |
| `internal/provisioner/docker_walg_sidecar.go` | Sidecar lifecycle (create/start/stop/remove) — alongside pg, same Docker network, both volumes mounted |
| `internal/provisioner/docker_walg_config.go` | Builds env map: `WALG_S3_PREFIX`, `AWS_ACCESS_KEY_ID/SECRET_ACCESS_KEY`, `AWS_ENDPOINT`, optional `WALG_LIBSODIUM_KEY`, `WALG_COMPRESSION_METHOD=lz4` |
| `internal/service/backup_adapter_docker_walg.go` | Phase 2 implementation that replaces Phase 1 `pg_basebackup` body. Same `BackupAdapter` interface — diff is internal. |
| `internal/service/wal_lag.go` | Surfaces "seconds since last successful WAL push" via `GET /backup/wal-lag` |

### Existing files to modify

| File | Change | Risk |
|---|---|---|
| `internal/provisioner/docker_postgresql.go:46` (`Provision`) | After container start, exec `ALTER SYSTEM SET archive_mode='on'`, `archive_command='wal-g wal-push %p'`, `wal_level='replica'`; `pg_reload_conf()`; **then restart container** because `archive_mode` requires restart, not reload | **HIGH** — wrong order = silent backup failures forever |
| `internal/provisioner/docker_postgresql.go:106` (`Deprovision`) | Stop sidecar BEFORE pg, remove sidecar, then pg, then both volumes. Drain WAL queue or accept loss (Risk #5). | high |
| `internal/provisioner/docker_postgresql.go:125` (`ConfigureBackup`) | Implement: docker exec sidecar `crontab -` to rewrite, no longer no-op | medium |
| `cmd/server/main.go` | Mount `/backup/wal-lag` route on `BackupHandler` | low |
| `internal/handler/backup.go` | Add `GetWalLag` handler — calls into adapter via optional `WalLagAdvertiser` interface (Docker only; K8s 501) | low |

### TDD red names (Phase 2)

- `t.Run("provision_sets_archive_command_then_restarts_container", ...)`
- `t.Run("provision_archive_mode_persists_after_pg_restart", ...)`
- `t.Run("sidecar_reads_credentials_from_env_at_start", ...)`
- `t.Run("sidecar_libsodium_key_default_present_for_enterprise_tier", ...)` (forces Risk #4)
- `t.Run("wal_push_failure_surfaces_to_alerting_service", ...)`
- `t.Run("backup_list_returns_walg_metadata_size_and_finished_at", ...)`
- `t.Run("pitr_restore_target_time_writes_recovery_signal", ...)`
- `t.Run("pitr_restore_target_xid_writes_recovery_signal", ...)` (forces RestoreTarget union)
- `t.Run("pitr_restore_rejects_target_time_predating_retention_window", ...)`
- `t.Run("deprovision_stops_sidecar_before_pg", ...)`
- `t.Run("deprovision_removes_walarchive_volume", ...)`
- `t.Run("wal_lag_returns_seconds_since_last_archive_success", ...)`
- `t.Run("wal_lag_no_data_when_no_backup_yet", ...)`

### Acceptance gates (Phase 2)

- All Phase 1 gates plus:
- `ghcr.io/excalibase/walg-sidecar:0.1.0` published, scanned by Snyk Container, 0 high+ findings
- `GET /api/projects/{id}/backup/wal-lag` (Docker mode), 501 on K8s (don't fake it)
- Sidecar restart resilience (chaos integration test): kill sidecar, archive_command queues fail, sidecar comes back, queue drains within 60s
- PITR integration test: restore-to-time produces exactly the expected row set (deterministic)
- Documented in `OPERATOR.md` §10: minimum disk space for `walarchive` volume (suggest 2x WAL generation rate × retention), restore RTO target, archive failure runbook

## 5. Phase 3 — Restore-into-new-project parity (3-4 days)

### New files

| Path | Purpose |
|---|---|
| `internal/service/backup_restore_orchestrator.go` | Multi-step restore state machine. Compensation actions per step (mirrors `provisioner.ProvisionContext`). Writes to `restore_jobs` table for resumability. |
| `internal/storage/postgres/migrations/000008_restore_jobs.up.sql` (+down) | `restore_jobs(id, source_project_id, new_project_id, status, current_step, target_time, created_at, updated_at, failure_reason)` |

### Existing files to modify

| File | Change | Risk |
|---|---|---|
| `internal/service/backup_adapter_docker_walg.go` `Restore` | Becomes a state-machine driver; returns immediately with `RESTORING`, async runs the rest. | high — async correctness |
| `internal/domain/dto.go:186` `RestoreRequest` | Add `RestoreTarget` union: `TargetTime *FlexTime | TargetXID *string | TargetLSN *string | TargetName *string`. Validate exactly one is set (or none = LATEST). | medium — public API change, but additive |
| `frontend/src/pages/BackupsPage.tsx` | UI for new target types (existing form already handles `targetTime`); add radio for target kind | medium |

### TDD red names (Phase 3)

- `t.Run("restore_returns_restoring_status_immediately", ...)`
- `t.Run("restore_failure_at_step_n_rolls_back_steps_1_through_n_minus_1", ...)`
- `t.Run("restore_resumable_after_platform_restart_mid_flight", ...)` ← pins Risk #3
- `t.Run("restore_source_project_unaffected_post_success", ...)`
- `t.Run("restore_source_project_unaffected_post_failure", ...)`
- `t.Run("restore_request_with_two_targets_rejected", ...)` (e.g. both targetTime and targetXid)
- `t.Run("restore_with_no_target_uses_latest", ...)`
- `t.Run("restore_pgdog_reconfig_within_5s", ...)`

### Acceptance gates (Phase 3)

- Restore RTO < 5 min for a 100MB DB (measured in CI, not just claimed)
- Source project unaffected (post-condition: `SELECT count(*)` returns same number before and after)
- PgDog routes the new project on the next config reload (NATS-published, < 5s observed)
- Platform restart mid-restore: restore continues from last successful step within 30s of restart
- Multi-target test matrix runs (`time`, `xid`, `lsn`, `name`, `latest`)

## 6. Five highest-risk decisions to pin NOW (before Phase 1 starts)

### Risk 1 — Where does the cron live?
**Trade-off:** in-process cron in the platform server (simple, atomic with DB) vs. crontab inside the WAL-G sidecar (durable across platform restarts, but split-brain if platform changes the schedule and sidecar wasn't reachable).
**Recommended default:** **In-process cron in the platform server, persisted to DB.** Reasons:
1. Schedule history is auditable (DB row).
2. One source of truth — sidecar just *runs* `wal-g backup-push` on cron-driven invocation, doesn't *own* schedule.
3. Restart resilience: on platform start, replay schedules from DB into the cron registry. ~30 lines of code, write the test (`schedule_persists_across_restart`).
4. Multi-instance platform: leader election (lease in DB, 60s TTL) so only one platform replica fires the cron. Cheap with Postgres advisory locks.

### Risk 2 — How does the sidecar get vault credentials?
**Trade-off:** env at start (simple, but creds rotate → restart needed) vs. sidecar runs the platform's vault client (creds refresh, but adds 8MB binary + auth flow).
**Recommended default:** **Env at start, with the platform writing creds via Docker `Update` API on rotation, triggering a sidecar restart.** WAL-G itself reads `AWS_*` only at process start anyway. Rotation is rare; sidecar restart on rotation is fine.

### Risk 3 — In-flight restores during platform restart?
**Trade-off:** tolerate orphan state vs. resumable state machine (correct, 2-3 days).
**Recommended default for Phase 1-2:** **fail loudly, leave orphan visible in `restore_jobs` with status FAILED, don't auto-recover.** Restore takes 2-30 min; platform restart is rare; operator can re-trigger. Phase 3 adds the resumable state machine.
**Concrete:** Phase 1 writes `restore_jobs` row with status RUNNING; on platform start, mark all RUNNING > 10min old as FAILED. 5 lines, not a state machine.

### Risk 4 — `WALG_LIBSODIUM_KEY` policy
**Recommended default:** **Tier-driven.** FREE/STANDARD: opt-in via project setting. ENTERPRISE: opt-out, default-on, key auto-generated and stored at `projects/{projectId}/backup/sodium-key` on first backup configuration. Platform never logs the key. Tier policy in `internal/config/tiers.go` owns this default.
**Concrete:** add `WALG_LIBSODIUM_KEY` to env builder if `tier == ENTERPRISE OR project.encryption_at_rest == true`. Default `project.encryption_at_rest = false` for FREE/STANDARD, `= true` for ENTERPRISE.

### Risk 5 — Deprovision drain semantics
**Recommended default:** **Drain with a 60s timeout, then force-stop.** Sidecar entrypoint exposes a "drain" exec command that flushes the queue and exits 0; if it doesn't exit in 60s, force-stop. Volume removal happens after sidecar removal regardless. Document the trade-off: 60s may not be enough for a large WAL backlog; high-write-rate operators should bump retention so the next provision (if it's a restore) can recover from R2.

## 7. Sequence dependencies

**Phase 0 → Phase 1:**
- `database_instances.deployment_mode` column exists in pg + sqlite
- New provisions write the column
- Migration tested forward + back on staging copy

**Phase 1 → Phase 2:**
- `BackupAdapter` interface frozen (no breaking changes — Phase 2 swaps *internals* of `DockerBackupAdapter`, not interface)
- `BackupRecordStore` is the source of truth (not JSON files); Phase 2 builds on this
- `RestoreRequest.RestoreTarget` union shipped in Phase 1 even though only `time` is wired (avoid public-API churn between phases)
- Risk decisions 1, 2, 4, 5 pinned in writing

**Phase 2 → Phase 3:**
- `ghcr.io/excalibase/walg-sidecar:0.1.0` published, integration tests pull from CI without auth (public package)
- `restore_jobs` table created (Phase 2 writes rows but doesn't drive state machine)
- WAL-G `wal-fetch` proven against R2 in integration tests (not just LocalStack)

**Phase 3 → done:**
- Risk 3 decision implemented (state machine + resumability)
- Studio UI updated for `RestoreTarget` union

## 8. Gaps in the original plan

### 8.1 Multi-tenancy / IDOR in `List`
1. **No auth check** at the adapter level — handler-level filters by current user's orgs but the adapter shouldn't trust the projectId blindly. Add explicit assertion that `inst.OrgID` matches the caller's org *inside* the service layer.
2. **Cross-project read** — if a malicious client passes a different projectId, R2 listing under the wrong prefix returns wrong data. Mitigate by always sourcing the prefix from `inst.ProjectID` (DB-loaded), never from the request.

### 8.2 Network policy
K8s mode has `internal/service/network_policy.go`. **Docker mode has no equivalent today.** Implications:
- Each project needs its own Docker network (`excalibase-{projectId}`). Confirm `docker_postgresql.go` does this — at a glance it doesn't.
- Sidecar joins ONLY the project's network.
- Add an integration test: sidecar on project A can't reach postgres on project B.

### 8.3 Monitoring/alerting wiring
- `archive_command = 'wal-g wal-push %p || /usr/local/bin/notify-failure %p %?'` where `notify-failure` writes to a shared volume, sidecar tails that volume.
- Alert published to `AlertingService.AddAlert` with `severity=CRITICAL, metric=archive_command_failure, projectId=X`.

### 8.4 Retention enforcement timing
- WAL-G has `wal-g delete retain FULL N` — runs on demand, not scheduled.
- Decision: a daily cron in the platform calls `wal-g delete retain FULL {tier.retentionDays}` per project. Same scheduler as backup runs.

### 8.5 R2 deletion on deprovision
**Recommend:** hard delete on deprovision IF `inst.DeletionProtection != true`. Add a `confirmDeleteBackups: true` field in the deprovision request to force the issue.

### 8.6 Backup encryption key rotation
**Decision:** no rotation in v1 (would break old backups). Document the constraint.

### 8.7 Testing in CI without R2
- LocalStack does NOT exercise R2's quirky multipart limits (5GB max part, 10000 parts).
- Real R2 must be exercised in `INTEGRATION_R2=1` gated tests at least once per phase.
- Add `make integration-r2` runnable locally with `.env.r2`.

### 8.8 Audit logging
Every backup trigger / list / restore is security-relevant. Wire each adapter call through `auditSvc.Log(actor, action, projectId, status)`. Add to acceptance gate.

### 8.9 Vault path
Plan says `projects/{org}/{project}/backup/s3`. Actual scheme is `projects/{projectId}/...` (no org component — `vaultProjectPrefix` in `internal/service/provisioning.go:567`). Use `projects/{projectId}/backup/s3` to match.

### 8.10 Free tier
**Recommend:** API rejection on FREE-tier `/backup/trigger` — simplest, hardest to bypass, clearest error.

## 9. Suggested commit sequence

One merged PR per row.

1. `feat(domain): add deployment_mode persistence to DatabaseInstance` (Phase 0)
2. `feat(provisioning): set DeploymentMode at provision time` (Phase 0)
3. `refactor(backup): extract BackupAdapter interface and K8s adapter` (Phase 1, no behavior change)
4. `feat(backup): docker adapter MVP (pg_basebackup + scratch volume)` (Phase 1)
5. `feat(backup): persistent cron scheduler with leader election` (Phase 1)
6. `feat(backup): RestoreTarget union with single-target validation` (Phase 1, public API)
7. `feat(backup): docker E2E test + studio works against docker mode` (Phase 1)
8. `feat(walg-sidecar): publish ghcr.io/excalibase/walg-sidecar:0.1.0` (Phase 2 pre-flight)
9. `feat(provisioner): docker postgres archive_mode + sidecar lifecycle` (Phase 2)
10. `feat(backup): WAL-G adapter replaces pg_basebackup MVP` (Phase 2)
11. `feat(backup): PITR target_time + target_xid + target_lsn` (Phase 2)
12. `feat(backup): wal-lag endpoint + alerting integration` (Phase 2)
13. `feat(backup): restore-into-new-project state machine` (Phase 3)
14. `feat(backup): retention enforcement daily cron` (Phase 3)

## 10. Open follow-ups (not blocking)

- Move K8s mode to WAL-G via CNPG plugin — DEFER unless Barman becomes a maintenance burden.
- MySQL adapter (Phase 4): `BackupAdapter` interface assumes one db per project — fine for MySQL.
- Mongo (Phase 5): `wal-g` Mongo backend uses oplog, not WAL — but interface still fits.
- Cross-region replication: configured at R2 bucket level, no platform code.
- Backup compression algorithm choice: WAL-G defaults to lz4 — good. Document operators with cheap storage can switch to zstd via env.
