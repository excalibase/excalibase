# Test plan — Excalibase Provisioning

This is the canonical inventory of what's tested today, what's
covered by which suite, and what gaps remain. Updated as part of the
ground-truth doc sync (May 2026).

## Reading the matrix

For each surface, four columns:

- **Surface** — what's under test
- **Where it lives** — repo + path
- **Tests today** — actual test invocations + counts
- **Gap** — what's not covered + concrete path to close it

`HIGH` = ship-blocker, `MED` = should-have, `LOW` = nice-to-have.

---

## 1. Backend Go server (this repo)

### 1.1 Auth + RBAC

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| argon2id password hashing | `internal/auth/password.go` | `auth_test.go` (covered) | — |
| Token issuance + verification | `internal/auth/auth_test.go`, `middleware_token_test.go` | covered, `-race` clean | — |
| Org / project RBAC matrix | `internal/auth/org_rbac.go` | `bootstrap_org_test.go` + `handler/org_test.go`, `org_member_test.go` | LOW: cross-tenant access denial — verify a project member of org A can't read org B's projects via crafted handler calls |
| Setup wizard (vault init + first admin) | `internal/handler/setup.go`, `setup_wizard_test.go` | covered + Playwright `setup-wizard.spec.ts` | — |
| Email verification + password reset | `internal/handler/auth.go` (email handlers), `internal/email/` | unit + Resend `live_test` (live send verified) | MED: end-to-end "click link in email → land on /reset" not exercised |

### 1.2 Provisioning + lifecycle

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| 9-stage K8s pipeline | `internal/provisioner/postgresql.go` | unit + `internal/k8s/*_integration_test.go` (k3s, build-tagged) | — |
| Rollback on stage failure | `internal/service/provisioning_test.go` (PopulatesFailureStageAndStep, RollbackDeletesNamespace, PersistsRollbackLog, RollsBackVaultWritesOnRoleCreationFailure) | covered | LOW: real-cluster rollback verification (currently MockClient) |
| Docker provisioner | `internal/provisioner/docker_postgresql.go` | `docker_postgresql_test.go`, `docker_client_integration_test.go` (`-tags=integration`) | LOW: per-project Docker network isolation (cross-project reachability test) |
| BYOC provisioning | `internal/handler/byoc.go` | `byoc_test.go`, `byoc.spec.ts` | — |
| Capacity pre-flight | `internal/k8s/capacity.go` | `capacity_test.go`, `provisioning_test.go::TestProvision_RefusesWhenClusterFull` | — |
| Tier enforcement | `internal/config/tiers.go` | `tiers_test.go`, `provisioning_test.go::ExceedsFreeTierLimit/Standard...` | — |
| Deployment-mode persistence | `internal/storage/{postgres,sqlite}/migrations/000007*` | `pg_test.go`, `sqlite_test.go::DeploymentMode_RoundTrips`, `LegacyRow_DefaultsToK8s`; `provisioning_test.go::SetsDeploymentMode_*` | — |

### 1.3 Backup + restore (Phase 0–3)

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| BackupAdapter dispatch | `internal/service/backup_adapter.go` | `backup_adapter_test.go` (7 cases: K8s/Docker dispatch, empty mode → K8s, unsupported → error, propagated error, list dispatch, restore dispatch) | — |
| K8s adapter (CNPG flow) | `backup_adapter_k8s.go` | implicit via `backup_test.go` (legacy tests still pass through dispatch) | — |
| Docker adapter MVP | `backup_adapter_docker.go` | `backup_adapter_docker_test.go` (8 cases: trigger basebackup, S3 prefix, persist record, FAILED on runner error, list IDOR, restore happy path, missing base errors, missing docker client errors) | — |
| Docker runner (pg_basebackup over docker exec) | `backup_runner_docker.go` | unit covered; integration via `backup_adapter_docker_integration_test.go` + `_r2_test.go` + `_restore_integration_test.go` | — |
| WAL-G runner (Phase 2) | `backup_runner_walg.go` | `backup_runner_walg_test.go` (5 cases against fake docker SDK) | HIGH: no integration test against a real WAL-G binary — the sidecar image is published but never exec'd in CI |
| S3 / R2 uploader | `backup_uploader.go` | unit + LocalStack E2E + **real R2 E2E** (gated `R2_ACCESS_KEY_ID`) | — |
| Backup scheduler (cron + leader) | `backup_scheduler.go` | `backup_scheduler_test.go` (5 cases: register/run, invalid cron, persists across restart, delete, not-leader-doesn't-fire) | LOW: real Postgres advisory lock under concurrent contention |
| Restore orchestrator (state machine) | `backup_restore_orchestrator.go` | `backup_restore_orchestrator_test.go` (7 cases: all-steps complete, fail-stops-and-persists, target-kind matrix, two-targets-rejected, sweep-stale, mid-flight Get, RestoreTarget union) | — |
| Wal-lag endpoint | `wal_lag.go` | `wal_lag_test.go` (3 cases: derives-from-records, no-records-returns-minus-1, K8s-returns-unsupported) | LOW: real WAL-G `wal-show` integration |
| BackupRecordStore (DB-backed) | `internal/storage/{postgres,sqlite}/sqlite_backup_records.go` | sqlite unit (3 cases) + pg integration (2 cases, build-tagged) | — |
| BackupSchedule store | same | sqlite unit (4 cases) + pg integration (3 cases) | — |
| RestoreJob store | same | sqlite unit (4 cases) + pg integration (2 cases) | — |
| End-to-end backup→S3→restore→query | `backup_adapter_docker_restore_integration_test.go` | **real testcontainers Postgres + LocalStack S3 round-trip; verifies seeded row survives the restore** | — |
| End-to-end backup against real R2 | `backup_adapter_docker_r2_test.go`, `backup_uploader_r2_test.go` | gated `R2_ACCESS_KEY_ID`; multipart + path-style + idempotent delete + List + gzip-magic verified | — |
| Continuous WAL archiving (Docker) | `provisioner/docker_postgresql.go::ConfigureArchive` (called from `Provision` when `req.Backup.Enabled`) + adapter's `RefreshWALArchive` / `uploadWALArchive` | `docker_postgresql_test.go` (2 cases: ALTER SYSTEM ordering, restart-after-alter) + integration via the PITR E2E below (proves /walarchive → S3 happens for real) | LOW: scheduled WAL refresh (currently only on backup trigger or explicit `RefreshWALArchive` call) |
| Docker PITR with target time / xid / lsn / name | `service/backup_adapter_docker.go::Restore` (recovery tar + WAL download) + `domain.RestoreRequest::RecoveryTarget` | unit `BuildRecoveryTar_*` (5 cases: no-target, time, xid, lsn, name) + `Restore_WithTargetTime_WritesRecoveryTar` + `Restore_NoTarget_NoRecoveryTar` + **`backup_adapter_docker_pitr_integration_test.go::PITR_TargetName` real E2E**: empty backup → seed with restore point → post-backup WALs uploaded → restore from empty backup pinned via `BackupID` with `TargetName='mark'` → only pre-mark row visible (proves the full archive_command → upload → download → recovery → promote pipeline) | LOW: target_xid + target_lsn + target_time covered by unit tests but the E2E only proves target_name. The recovery directive logic is identical for all four kinds. |

### 1.4 Scheduling + DB persistence

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| Migrations forward + back | `internal/storage/{pg,sqlite}/migrations/` (9 files each) | applied automatically on store init; structural validation in `migrate_test.go` (sqlite) | LOW: down-migration drift test (rare in practice) |
| SQLite store | `internal/storage/sqlite/` | `sqlite_test.go`, `sqlite_orgs_test.go`, `sqlite_backup_records_test.go`, `sqlite_backup_schedules_test.go`, `sqlite_restore_jobs_test.go`, `coverage_gap_test.go` | — |
| Postgres store | `internal/storage/postgres/` | `pg_test.go` + 4 backup-related, all `-tags=integration` | LOW: large-row scaling (hundreds of orgs/projects) |
| FileSystemStore (vault metadata) | `internal/storage/filesystem.go` | `filesystem_test.go` (incl. roundtrip + LegacyRow_DefaultsToK8s) | — |

### 1.5 Edge functions (Deno)

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| Function CRUD | `internal/handler/function.go` | `function_test.go`, `function_integration_test.go` (real Deno subprocess, `-tags=integration`) | — |
| Multi-file source bundling (esbuild) | `internal/edgefn/store.go`, `client.go` | `client_test.go`, `function_test.go` | — |
| Secrets store (vault-backed) | `internal/edgefn/secrets.go` | `secrets_test.go` | — |
| Public invoke `POST /functions/v1/{projectId}/{fnId}` | `internal/handler/function.go` | `function_integration_test.go` | LOW: rate-limit edge cases (busy-loop attack) |
| `verifyJwt` gating | same | covered | — |
| Log streaming SSE (`GET /api/projects/{id}/functions/{fnId}/logs`) | `internal/handler/function.go` | `function_logs_test.go`, `sse_test.go` | LOW: client-disconnect cleanup under load |
| K8s mode: per-project Deno pod | `internal/edgefn/integration_test.go` | covered | — |
| Deno runtime image | `deno-server/` | smoke via `function_integration_test.go` | MED: dedicated Dockerfile vulnerability scan (Snyk Container) — workflow not yet wired |

### 1.6 Storage feature

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| Bucket CRUD | `internal/storagesvc/service.go` | `service_test.go` | — |
| Object key building + traversal protection | `internal/storagesvc/r2_client.go` | `r2_client_test.go` (`ObjectKey_*` covers prefix + traversal denial) | — |
| Quota tracking | `internal/storagesvc/service.go` | `service_test.go::TestQuotaEnforced` | — |
| Object upload (presigned URL) | `internal/handler/storage.go` | unit covered | LOW: full E2E with real R2 — currently relies on the existing real-user-flow shell script |
| Object download (redirect) | same | unit covered | — |
| Public URL routing | same | unit covered | — |

### 1.7 Realtime (CDC publication management)

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| Publication membership API (`GET /tables`, `PUT/DELETE /tables/{schema}/{table}`) | `internal/handler/realtime.go` | `realtime_test.go` | — |
| Enable-all / disable-all toggles | same | covered | — |
| Configurable publication name (`REALTIME_PUBLICATION_NAME`) | same | covered | — |
| **Watcher integration** (lives in `excalibase-watcher-go`) | sister repo | not in this repo's test suite | **CROSS-REPO** — own tests there, contract pinned via the projectId convention memory |

### 1.8 PgDog connection pooler

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| NATS-triggered config reload | `internal/service/pgdog_notifier.go` | `tests/pgdog/test-nats-reload.sh` | LOW: Go-side unit test for the publish path |
| Config table CRUD (`pgdog_databases`, `pgdog_users`) | `internal/storage/{pg,sqlite}/pg_pgdog.go` | `tests/pgdog/test.sh` (shell-based) | MED: convert to Go integration test |
| R/W splitting routing | sister repo (`excalibase/pgdog` fork) | not in this repo's tests | **CROSS-REPO** — see `pgdog/test.sh` for smoke |

### 1.9 Email

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| Sender interface + noop fallback | `internal/email/sender.go` | `sender_test.go` | — |
| SES sender | `internal/email/ses_sender.go` | unit | LOW: SES sandbox is not configured, so live SES path untested in this repo |
| **Resend sender** (Phase 2026-05) | `internal/email/resend_sender.go` | `resend_sender_test.go` (7 cases); **`resend_live_test.go` ran against real Resend with verified `support@excalibase.io` domain → message id `62e825ff-...`** | — |
| EMAIL_PROVIDER switch | `cmd/server/main.go::buildEmailSender` | not directly unit-tested but covered by smoke build + manual run | LOW: a switch-case unit test for unknown values → noop |
| Templates (verification, reset, invite) | `internal/email/templates.go` | implicit (rendered in handler tests) | LOW: dedicated golden-file test for HTML/text rendering |

### 1.10 Vault

| Surface | Where it lives | Tests today | Gap |
|---|---|---|---|
| In-process (Shamir + bbolt) | `internal/vault/vault.go`, `pkg/vault/vault_test.go` | covered | — |
| HTTP client | `internal/vaultclient/client.go` | `client_test.go` | — |
| Standalone vault HTTP service | `internal/handler/vaultapi/handler.go` | `handler_test.go` | — |
| Setup wizard atomic init | `internal/handler/setup.go` | `setup_wizard_test.go`, Playwright `setup-wizard.spec.ts` | — |

---

## 2. Frontend (this repo)

### 2.1 Vitest unit tests (`frontend/src/**/*.test.{ts,tsx}`)

| Surface | Tests today | Gap |
|---|---|---|
| Hooks (`useProvisioning`, `useOrgs`, `useFunctions`, `useBackups` etc.) | 13 test files; ≥80% coverage per CLAUDE.md | — |
| Components (Button, AuthGuard, etc.) | covered | LOW: snapshot test for major page layouts (catches accidental UI shifts) |

### 2.2 Playwright E2E (`frontend/e2e/*.spec.ts`)

27 spec files, 131 passing + 4 skipped (1 on `REAL_E2E=1` for `real-user-flow.spec.ts`, 3 on `STUDIO_LIVE=1` for `studio-live-data.spec.ts`).

| Spec | Surface | Notes |
|---|---|---|
| `setup-wizard.spec.ts` | first-time vault init + admin | mocked vault |
| `auth-cookie-flow.spec.ts` | login + refresh + logout | mocked auth |
| `auth-users.spec.ts` | platform-admin user CRUD | mocked |
| `orgs.spec.ts` | org list + create + empty state | mocked |
| `vault.spec.ts` | secrets list + reveal + sealed redirect | mocked |
| `byoc.spec.ts` | BYOC mode UI | mocked |
| `deployment-mode.spec.ts` | mode selector toggles UI | mocked |
| `tables.spec.ts`, `indexes.spec.ts`, `triggers.spec.ts`, `types.spec.ts`, `extensions.spec.ts`, `roles.spec.ts`, `rls.spec.ts`, `migrations.spec.ts`, `sql-editor.spec.ts` | studio DB action surfaces | mocked schema API; verifies UI behaviour, not actual DB action |
| `realtime.spec.ts` | per-table CDC toggle UI | mocked |
| `edge-functions.spec.ts`, `functions.spec.ts` | function CRUD UI | mocked |
| `advisors.spec.ts` | advisor recommendations UI | mocked |
| `api-info.spec.ts` | per-project API info modal | mocked |
| `log-explorer.spec.ts` | logs viewer | mocked |
| `settings.spec.ts` | project settings (delete protection, schedule) | mocked |
| `navigation.spec.ts` | platform vs project sidebar split | mocked |
| `backups.spec.ts` (Phase 1E) | trigger / list / restore form / PITR mode toggle / payload assertions | mocked |
| `real-user-flow.spec.ts` | full flow against a live AIO stack | gated `REAL_E2E=1` (currently skipped in CI) |

| Coverage gap | Severity |
|---|---|
| Studio actions don't actually exercise the GraphQL/REST data plane — all schema specs use `page.route('**/api/...')` mocks. We test the UI, not the data integrity. | MED — by design (sister-repo coverage), but should be called out |
| Backups spec is mock-based; doesn't exercise a real Docker-mode dev stack. The new `backup_adapter_docker_restore_integration_test.go` covers the backend side. | LOW (deferred to Phase 1E follow-up) |
| Real-user-flow spec runs against AIO; no per-PR run | MED — the gate is opt-in, no CI signal until manually flipped |

---

## 3. Cross-repo integration boundaries

Sister repos own their own behavior tests (graphql in particular has very good Java/Spring Boot test coverage). This section is about the **wires** — what this repo provisions, secrets, and routes that sister services then *connect to*. The risk surface isn't "does graphql do GraphQL right"; it's "when graphql tries to dial CNPG via PgDog using the JWT we minted, do all four pieces actually agree".

The platform AIO ships **5 service images**, of which 3 come from sister repos and 2 are owned here. PgDog is a sixth dependency, run as a fork of the upstream pooler.

### 3.1 GraphQL (`excalibase-graphql`) — wires into our infra

Graphql connects to four pieces this repo provisions/owns. Sister-repo tests verify graphql does GraphQL right; *this* repo's tests verify each wire is dialled correctly.

| Wire | What this repo provides | Risk if it drifts | Coverage in this repo |
|---|---|---|---|
| **CNPG database connection** | per-project Postgres cluster, accessed via PgDog pooler at `pgdog.{platform-ns}.svc:6432`, db name = `projectId`, user = `app`, password rotated via vault | wrong DSN → graphql fails to start; rotated password not in pgdog_users → graphql gets auth-rejected; pgdog routes to wrong cluster (name collision) | `pgdog_notifier_integration_test.go` (register writes correct host + db_name + role), `contract_test.go::TestContract_ProjectID_FormatStable` (db name shape), `tests/e2e-aio-self.sh` F-series (smokes a real graphql query through pgdog) |
| **JWT public key** | `pki/signing/public` in vault; graphql fetches via `/api/projects/{id}/info` (or vault HTTP if VAULT_URL set) to verify subscription tokens | key rotated without graphql cache invalidation → all subscriptions reject; vault path renamed → graphql can't find the key | `contract_test.go::TestContract_JWTPaths_Stable`, `function_test.go` covers the same `pki/signing/public` read path with a different consumer (edge functions) so any rename breaks both code paths simultaneously |
| **NATS CDC subscriptions** | NATS at `nats.{platform-ns}.svc:4222`; graphql subscribes to `cdc.{projectId}.>` (no orgSlug); JWT-gated by graphql itself | wrong subject prefix → silent: subscriptions never receive events; orgSlug accidentally added back → graphql sees nothing | `contract_test.go::TestContract_NATS_CDCSubjectShape`, `TestContract_NATS_NoOrgSlug`; integration verified by `tests/e2e-aio-self.sh` realtime sections |
| **Provisioning REST API** | `/api/projects/{id}/info` returns project metadata graphql needs (db url, schemas, JWT public key, project tier); auth via `provisioning-pat` (long-lived PAT minted at platform setup) | response shape change → graphql misreads the metadata; PAT auth disabled → graphql gets 401 on every project lookup | `provisioning_info_test.go` (handler-level shape + auth), `aio-e2e` job exercises the full provision-then-graphql-connects flow |

**Likely failure modes if a sister-repo release drifts here:**
- Graphql rebuild with `cdc.{orgSlug}.{projectId}.*` → realtime subscriptions silently break. `TestContract_NATS_NoOrgSlug` fires if someone tries the same change in this repo; AIO E2E catches actual sister-side change.
- Graphql cached JWT public key beyond rotation TTL → AIO E2E catches if rotation flow exists; otherwise this is a long-tail prod issue.
- Provisioning-info handler renames a JSON field → handler test catches the field rename; AIO E2E catches end-to-end mismatch.

### 3.2 Auth IMS (`excalibase-auth`) — wires into our infra

| Wire | What this repo provides | Risk if it drifts | Coverage |
|---|---|---|---|
| **Vault JWT private key** | `pki/signing/private` written by setup wizard; auth IMS reads to mint JWTs | wizard never wrote → auth IMS can't mint; rotated path → auth panics on startup | `setup_wizard_test.go`, `vault_test.go`, `contract_test.go::TestContract_JWTPaths_Stable` |
| **Provisioning REST → user/org/project lookups** | `/api/admin/users`, `/api/orgs`, `/api/projects/{id}/info` serve the auth UI; PAT auth | admin-handler RBAC tightened too far → auth's own setup flow blocked | `admin_test.go`, `org_test.go`, `org_member_test.go`, `provisioning_info_test.go` |
| **Local stub auth in self-hosted mode** | when EXTERNAL_AUTH_URL is unset, the stub at `internal/handler/auth.go` mints + verifies JWTs in-process | stub diverges from IMS contract → upgrades from self-hosted to cloud break | `auth_test.go`, `register_test.go`, `auth-cookie-flow.spec.ts` (Playwright) |

Local stub coverage in this repo is comprehensive; full IMS integration relies on `aio-e2e` running against the real `excalibase-auth` image.

### 3.3 Watcher (`excalibase-watcher-go`) — wires into our infra

| Wire | What this repo provides | Risk if it drifts | Coverage |
|---|---|---|---|
| **Per-project PostgreSQL role** | `cdc_watcher` role created during provisioning; `LOGIN`, `REPLICATION`, `pg_read_all_data`, can use replication slots | role privileges tightened wrong → watcher CrashLoops on startup with "permission denied to use replication slots" | `internal/provisioner/postgresql.go::stageRoleCreation` + tests in `provisioning_test.go`; documented incident in OPERATOR.md §8 |
| **Publication membership** | publication `cdc_watcher_pub` (configurable via `REALTIME_PUBLICATION_NAME`); watcher reads from this pub only | publication name renamed → watcher dies; publication owned by wrong role → watcher can't ALTER membership when studio toggles tables | `realtime_test.go` (publication CRUD), `contract_test.go` could pin the default publication name (TODO add) |
| **NATS publish target** | watcher writes to `cdc.{projectId}.{schema}.{table}`; we deploy it with this projectId baked in via Helm values | watcher publishes to wrong subject → graphql sees nothing | `contract_test.go::TestContract_NATS_CDCSubjectShape`, `TestContract_NATS_NoOrgSlug`; deploy-side via `internal/k8s/helm.go` tests |
| **Image tag** | hardcoded in `internal/provisioner/postgresql.go::watcherImage` per memory `gotchas_email_storage_r2.md` | image tag drift between provisioning-rev and watcher-rev → watcher pod ImagePullBackOff or runs old code | bump rule documented in OPERATOR.md §7; `aio-e2e` catches mismatches if it runs on each release |

### 3.4 PgDog (`github.com/excalibase/pgdog` fork) — wires into our infra

| Wire | What this repo provides | Risk if it drifts | Coverage |
|---|---|---|---|
| **Config tables** | `pgdog_databases` (name, host, port, database_name, role, shard, read_only) + `pgdog_users` (name, database, password) on platform-db | column added/renamed by either side without coordinated bump → PgDog fails to read config | `pgdog_notifier_integration_test.go::RegisterCluster_writes_DB_rows` asserts the column shape against a real Postgres |
| **NATS reload signal** | publish on subject `pgdog.config.reload` after every register/deregister | subject renamed → PgDog never re-reads → new projects unreachable until pgdog restart | `contract_test.go::TestContract_PgDogReloadSubject`, `pgdog_notifier_integration_test.go::RegisterCluster_publishes_reload_to_NATS` |
| **Idempotent register** | upsert semantics on (name, role, shard) so password rotation just replaces the row | UNIQUE violation on rotation → admin-page password rotation fails for existing projects | `pgdog_notifier_integration_test.go::RegisterCluster_idempotent_upsert` |
| **R/W routing** | role='primary' for the rw cluster service, role='replica' read_only=true for the ro service | wrong host string format → PgDog can't resolve the CNPG service | `RegisterCluster_writes_DB_rows` asserts host format `{projectId}-postgres-rw.{namespace}.svc.cluster.local` |

If our fork drifts (upstream PgDog merges support for a different reload signal): bump the fork to absorb upstream → adjust the subject const in `pgdog_notifier.go` → update the contract test → run `tests/pgdog/test.sh` against a live cluster as final smoke.

### 3.5 Storage / NoSQL / future engines

- **Storage** (object/file) — covered in `internal/storagesvc/` (this repo). User-facing E2E partly covered by `tests/e2e-aio-self.sh` F23.
- **NoSQL** (MongoDB, etc.) — **not implemented yet**. Future engine; the `BackupAdapter` interface already accepts arbitrary `domain.DatabaseType`, so the adapter pattern extends cleanly.
- **MySQL** — provisioner stub exists; no live tests. Future engine.

---

## 4. CI / cluster surfaces

### 4.1 What runs in CI

| Job | Tests | Trigger |
|---|---|---|
| `go-test` | `go test ./internal/... -short -race -count=1` (110 tests) | every push |
| `go-vet` | `go vet ./...` + `go vet -tags=integration ./...` | every push |
| `playwright` | `npx playwright test` (26 specs, 131 + 1 skip) | every push |
| `snyk-code` | `snyk code test --severity-threshold=low` | every push |
| `sonarcloud` | Sonar scan with go coverage | every push |

### 4.2 What doesn't run in CI

| Test | Why | Severity |
|---|---|---|
| `-tags=integration` Go suite | testcontainers needs Docker daemon; CI doesn't have one yet | MED |
| Real R2 E2E | requires `R2_ACCESS_KEY_ID` secret in CI | MED — but the gate works locally |
| Real Resend live test | requires `RESEND_API_KEY` secret in CI | MED |
| Real K8s integration (k3s testcontainers) | same Docker dependency | MED |
| `tests/e2e-aio-self.sh` (full AIO smoke) | needs a live AIO stack | LOW (manual run before release) |
| `tests/pgdog/test.sh` | needs running PgDog + Postgres | LOW (manual) |
| Sister-repo tests (graphql, auth, watcher) | not this repo | N/A |

---

## 5. Manual / live verification done in May 2026

Rotating these to a checklist so they're not lost:

- [x] Resend `support@excalibase.io → vuduc047@gmail.com` — message id captured
- [x] R2 backup full pipeline (`pg_basebackup` → multipart upload → download → gzip magic verified)
- [x] Docker restore E2E (testcontainers Postgres → backup → S3 → restore → `SELECT n FROM smoke` returns 4242)
- [ ] Helm template render of `platform-base` with `platformDB.backup.endpointURL` set — chart YAML renders cleanly (verified) but actual `helm upgrade` against minikube not run
- [ ] Live PgDog config reload via NATS publish — `tests/pgdog/test-nats-reload.sh` ran in earlier session per memory; not re-verified
- [ ] Vault-from-shamir-shares unseal end-to-end — covered by setup-wizard spec mock; not exercised against a sealed bbolt vault recently

---

## 6. Highest-priority gaps (next session backlog)

In severity order:

1. ~~**HIGH — Continuous WAL archiving for Docker mode**~~ **DONE (May 2026):** `ConfigureArchive` is now called from `Provision` when `req.Backup.Enabled`. archive_command writes to `/walarchive` inside the container; `uploadWALArchive` ships them to S3 on every backup trigger (or via `RefreshWALArchive` out-of-band). Verified by the PITR E2E test.

2. ~~**HIGH — Docker PITR (`targetTime`/`xid`/`lsn`) execution**~~ **DONE (May 2026):** Adapter writes `recovery.signal` + `postgresql.auto.conf` (with `recovery_target_*` + `recovery_target_action='promote'` + `restore_command`) into the new container before start. `downloadWALsIntoContainer` fetches archived WALs from S3 and places them in `wal_restore/`. Postgres recovers, hits the target, and promotes. Proven end-to-end with the `mark` restore-point scenario in `PITR_TargetName`.

3. ~~**MED — Sister-repo contract tests**~~ **DONE (May 2026):** `internal/service/contract_test.go` pins ProjectID format (regex + DNS-1123 compliance), vault path shape, NATS CDC subject pattern (`cdc.{projectId}.>` — projectId-only, no orgSlug), PgDog reload subject, and JWT vault paths (`pki/signing/{public,private}`). 6 cases, runs in default `go test`. Failure surfaces wire-format drift before sister repos break in production.

4. ~~**MED — Studio E2E against real data plane**~~ **DONE (May 2026):** `frontend/e2e/studio-live-data.spec.ts` — gated on `STUDIO_LIVE=1` + `E2E_API_URL` + `E2E_PROJECT_ID` + `E2E_PAT`. Three specs: table list reflects real schema, SQL editor CREATE → INSERT → SELECT → DROP round-trip, realtime subscribe + INSERT + receive event. Skips cleanly when env unset.

5. ~~**MED — `tests/e2e-aio-self.sh` runs in CI**~~ **DONE (May 2026):** new `aio-e2e` job in `.github/workflows/ci.yml` uses `medyagh/setup-minikube`, helm-installs the platform-aio chart, runs the shell script. 30-min timeout, dumps platform logs on failure.

6. ~~**MED — Convert pgdog shell tests to Go**~~ **DONE (May 2026):** `internal/service/pgdog_notifier_integration_test.go` — testcontainers Postgres + NATS, exercises register / upsert / deregister / no-NATS-silent paths. 5 sub-tests, ~2s total. The shell script stays for live-cluster smoke tests.

7. ~~**LOW — Real Resend / R2 / k3s integration in CI**~~ **DONE (May 2026):** added `real-r2-integration`, `real-resend-integration`, and `go-integration` jobs in `.github/workflows/ci.yml`. R2 + Resend jobs gate on secrets being set in GH Actions — operator action: configure under repo Settings → Secrets and Variables → Actions:
   - `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_ENDPOINT`, `R2_BUCKET`
   - `RESEND_API_KEY`, `RESEND_TEST_FROM` (verified domain), `RESEND_TEST_TO` (or use `delivered@resend.dev` default)

   `go-integration` runs the full `-tags=integration` Go suite via ubuntu-latest's Docker daemon (no extra setup — testcontainers-go works out of the box).

---

## 7. How to run each suite locally

```bash
# Backend Go: short, race-checked
cd server-go && ~/go-sdk/go/bin/go test ./internal/... -short -race -count=1

# Backend Go: integration (needs Docker daemon)
~/go-sdk/go/bin/go test -tags=integration ./internal/... -race -timeout=10m

# Real R2 (requires creds)
export R2_ACCESS_KEY_ID=... R2_SECRET_ACCESS_KEY=... \
       R2_ENDPOINT=https://<account>.r2.cloudflarestorage.com R2_BUCKET=excalibase-backups
~/go-sdk/go/bin/go test -tags=integration ./internal/service/ -run R2_E2E -v

# Real Resend (requires creds + verified domain)
export RESEND_API_KEY=... RESEND_TEST_FROM=support@yourdomain.com RESEND_TEST_TO=you@gmail.com
~/go-sdk/go/bin/go test -tags=integration ./internal/email/ -run Live -v

# Frontend Vitest
cd frontend && npm test

# Playwright (full suite)
npx playwright test

# Playwright (single spec)
npx playwright test e2e/backups.spec.ts --reporter=list

# AIO end-to-end (manual; needs minikube)
./tests/e2e-aio-self.sh

# Sonar local
./scripts/sonar-local.sh
```

---

## 8. Maintenance contract

When adding a feature, update **two columns** in this doc:

- The relevant section's table — drop a row for the test that ships with the feature
- Section 6 if a known gap is introduced — operators reading this should see it

When ground-truth drifts (test count, spec count, missing endpoint), the doc fix is part of the same PR that drifted them. Doc-vs-code drift was a real problem that bit us in May 2026 — see `docs: sync docs to match shipped Phase 0–3` (a519d44) for the correction. Don't let this doc rot.
