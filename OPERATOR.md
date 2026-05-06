# Excalibase operator runbook

This is the day-1 to day-N reference for running an Excalibase platform. It assumes you've cloned the chart repo (`excalibase-service`) and have a Kubernetes cluster (any distro: managed EKS/GKE/AKS, RKE2, k0s/k3s, or single-node minikube).

## 1. Install

```bash
# Sized for 24 CPU / 64GB nodes; tune CPU/mem under .resources blocks for smaller hardware.
helm upgrade --install platform ./charts/platform-aio \
  --namespace excalibase-platform --create-namespace \
  -f charts/platform-aio/values-prod.yaml
```

After the rollout finishes:

```bash
kubectl -n excalibase-platform get pods                # all pods Running
kubectl -n excalibase-platform get cluster             # CNPG status: "Cluster in healthy state"
kubectl -n excalibase-platform get secret platform-bootstrap -o jsonpath='{.data.admin-pass}' | base64 -d ; echo
```

The last command prints the **bootstrap admin password**. Log in once at `https://<your-host>/login` (username `admin`), then immediately:
1. Mint a long-lived PAT for CI/scripts (Settings → Personal access tokens).
2. Rotate or delete the bootstrap admin user (`/admin`) once a real platform_admin user exists.

## 2. Capacity check before provisioning

The platform refuses provisions that wouldn't fit the cluster. To plan ahead:

```bash
curl -sf -H "Authorization: Bearer $PAT" https://<host>/api/capacity | jq
```

Read the `tiers.<tier>.projectsCanFit` field — that's how many additional projects of that tier you can add. If it's 0 you'll see `limitedBy: "cpu"` or `"memory"` so you know which dimension to scale.

The 15% safety headroom is set via `CAPACITY_HEADROOM_PERCENT` env. Set to 0 if you want to plan against full Allocatable (not recommended).

## 3. Drop a project (admin override)

The studio's **Platform Admin** page (`/admin`) has the buttons. CLI equivalent:

```bash
# Bypasses deletion_protection. Audited.
curl -X DELETE -H "Authorization: Bearer $PAT" \
  https://<host>/api/admin/projects/proj-abc123
```

## 4. Revoke an org (cascade-drop all its projects)

```bash
# ?cascade=true is required — without it the call refuses to avoid
# orphaning projects whose org row would still exist.
curl -X DELETE -H "Authorization: Bearer $PAT" \
  "https://<host>/api/admin/orgs/<orgId>?cascade=true"
```

Returns `200` if all projects dropped cleanly; `207 Multi-Status` if any failed (the response body lists which).

## 5. Query logs

Centralised across services via Loki (collected by the platform's `loki-stack`).

```bash
# Last 15 min of auth errors, all tenants
curl -sf -H "Authorization: Bearer $PAT" \
  "https://<host>/api/admin/logs?service=auth&since=15m&q=ERROR" | jq

# Postgres logs for one project
curl -sf -H "Authorization: Bearer $PAT" \
  "https://<host>/api/admin/logs?service=cnpg&projectId=proj-abc123&since=1h" | jq

# Per-project Deno runtime
curl -sf -H "Authorization: Bearer $PAT" \
  "https://<host>/api/admin/logs?service=deno&projectId=proj-abc123" | jq
```

Services available: `auth | graphql | provisioning | watcher | deno | cnpg | all`. Per-project services (`watcher`, `deno`, `cnpg`) require `projectId`.

For ad-hoc LogQL, port-forward Grafana directly:

```bash
kubectl -n monitoring port-forward svc/monitoring-grafana 33000:80
# admin user pass:
kubectl -n monitoring get secret monitoring-grafana -o jsonpath='{.data.admin-password}' | base64 -d ; echo
# Open http://localhost:33000  →  Excalibase folder
```

## 6. Backup + restore

### K8s mode (CNPG + Barman → R2)

#### Credential flow (what actually happens at runtime)

```
vault://backup/s3                       ← canonical source (admin uploads once)
        │
        │ provisioning.go:268 reads at provision time
        ▼
req.Backup.S3 (in-memory, scoped to one provision call)
        │
        │ provisioner/postgresql.go:40 fans out to:
        ▼
K8s secret  excalibase-{org}-{projectId}/backup-s3-creds
                ACCESS_KEY_ID, ACCESS_SECRET_KEY
        │
        │ crd_builder.go:223 references it from:
        ▼
CNPG Cluster CRD → barmanObjectStore.s3Credentials.{accessKeyId,secretAccessKey}
        │
        │ CNPG operator + Barman read the project-namespace secret
        ▼
s3://excalibase-backups/{projectId}/cloud/{base,wals}/...
```

**Properties to keep in mind when operating:**

- The K8s secret is **project-namespace-scoped**, not cluster-scoped. Each
  project's CNPG operator reads only its own copy; no central secret holds
  every project's credentials.
- The platform Go process is the only thing that ever reads vault `backup/s3`.
  CNPG never touches vault — it only reads the project-namespace K8s secret.
- **Rotation:** `vault put backup/s3 ...` rotates the source. New provisions
  pick up the new value; *existing* per-project K8s secrets keep the old
  value until re-provisioned or rotated by tooling. There is no auto-rotate
  loop today; if you rotate R2 keys, you must walk every project namespace
  with `kubectl create secret backup-s3-creds --dry-run=client -o yaml | apply`.
- **Vault uninitialised fallback:** if vault is sealed / no `backup/s3`,
  `provisioning.go` falls through to env-driven `BackupDefaults` populated
  from the cluster-scoped `r2-creds` K8s secret on the `provisioning`
  deployment. Same R2 destination, but the master creds are visible to
  anyone with `get secret -n excalibase-platform r2-creds`. **This is the
  state of the current minikube** — initialise the vault to tighten this.
- **Platform-db cluster has no backup configured** in the deployed
  `charts/platform-base/values.yaml`. Per-project clusters are protected;
  the platform DB itself is not. Single-line fix: wire
  `.Values.platformDb.backup.s3` to the same secret. Not done by default.

#### R2 layout (Barman convention)

```
s3://excalibase-backups/
  {projectId}/cloud/base/{timestamp}/{backup.info, data.tar.gz}
  {projectId}/cloud/wals/{timeline}/{walSegment}.gz
```

`serverName: cloud` is hardcoded in `crd_builder.go:165`; `destinationPath`
is `s3://excalibase-backups/{projectId}`. Inspect with `aws s3 ls
s3://excalibase-backups/{projectId}/cloud/ --endpoint-url https://<account>.r2.cloudflarestorage.com`.

#### CLI

```bash
# List snapshots a project has
curl -sf -H "Authorization: Bearer $PAT" \
  https://<host>/api/provision/proj-abc123/backup/list | jq

# Trigger a fresh backup right now
curl -X POST -H "Authorization: Bearer $PAT" \
  https://<host>/api/provision/proj-abc123/backup/trigger

# Restore: provision a NEW project seeded from a backup. The original project is untouched.
curl -X POST -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"newProjectId":"proj-restored","backupId":"<backup-id>"}' \
  https://<host>/api/provision/proj-abc123/backup/restore
```

PITR (point-in-time recovery) reuses the same flow with a `targetTime`
field in the body.

### Docker mode (in-process backup adapter)

> Status: Phases 0–3 landed (May 2026). PITR end-to-end verified
> with three target kinds: `targetName`, `targetXID`, `targetTime`
> (E2E tests in `internal/service/backup_adapter_docker_pitr_integration_test.go`).
> R2/S3 credentials must be set via `BACKUP_DEFAULT_*` env (or
> legacy `R2_*`) for the Docker adapter to register; without them
> `/backup/trigger` returns `501 backup not supported for this deployment mode`.

**What actually ships (vs. the original sidecar design):** what
landed is the simpler "platform-driven" pipeline, not the WAL-G
sidecar from the original plan doc. The sidecar Dockerfile +
publish workflow are kept for the Phase 4 polyglot future (MySQL,
MongoDB) but aren't on the critical path today.

How a Docker-mode project's backup actually works:

1. **At provision time** — when `req.Backup.Enabled`, the
   provisioner runs `ConfigureArchive` (postgresql.go): `ALTER
   SYSTEM SET wal_level='replica'; archive_mode='on';
   archive_command='cp %p /walarchive/%f'`, then restarts the
   container so archive_mode takes effect. `/walarchive` is a
   directory inside the postgres container (created with postgres
   user ownership pre-init).

2. **On `POST /backup/trigger`** — `DockerBackupAdapter.TriggerManual`
   runs `pg_basebackup -F tar -X fetch -z` via `docker exec`, streams
   the gzipped tar into an S3 multipart upload at
   `s3://{bucket}/backups/{projectId}/manual/{id}.tar.gz`. After
   the basebackup uploads, it forces `pg_switch_wal()` + `CHECKPOINT`,
   pulls `/walarchive` out via `CopyFromContainer`, and uploads
   each WAL segment to `s3://{bucket}/backups/{projectId}/wals/{name}.gz`.

3. **On `POST /backup/restore` with a recovery target** —
   `DockerBackupAdapter.Restore` creates a stopped postgres container,
   gunzips + extracts the basebackup tar into `/var/lib/postgresql/data`,
   downloads every archived WAL segment + extracts them into
   `/var/lib/postgresql/data/wal_restore/`, writes
   `recovery.signal` + `postgresql.auto.conf` (with
   `recovery_target_*`, `recovery_target_action='promote'`, and
   `restore_command='cp /var/lib/postgresql/data/wal_restore/%f %p'`),
   then starts the container. Postgres replays WALs from
   `pg_wal/` (basebackup's bundled WALs) + `wal_restore/`, hits
   the target, and promotes.

WAL freshness: WAL segments are uploaded synchronously at backup
trigger time. For continuous near-real-time PITR (sub-minute
recovery window), call `RefreshWALArchive` from a periodic cron
or wire it into the platform-side scheduler. Today the gap between
shipped WAL and backup trigger is "next backup" — set a tighter
`backup_schedule` to shrink it.

Endpoints:

```bash
# Manual trigger — synchronous; returns COMPLETED with the message id
# once basebackup + WAL upload finish (typical: 5–30s for small DBs).
curl -X POST -H "Authorization: Bearer $PAT" \
  https://<host>/api/projects/proj-abc123/backup/trigger

# Schedule CRUD
curl -X POST -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"cron":"0 2 * * *","retentionDays":7,"enabled":true}' \
  https://<host>/api/projects/proj-abc123/backup/schedule

# WAL lag (Docker only — K8s returns 501)
curl -H "Authorization: Bearer $PAT" \
  https://<host>/api/projects/proj-abc123/backup/wal-lag

# Restore (returns RestoreJob with status RUNNING; poll GET below)
curl -X POST -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"newProjectId":"proj-restored","targetTime":"2026-05-01T03:00:00Z"}' \
  https://<host>/api/projects/proj-abc123/backup/restore

curl -H "Authorization: Bearer $PAT" \
  https://<host>/api/projects/proj-abc123/backup/restore/<jobId>
```

`RestoreRequest` accepts at most one of `targetTime`, `targetXid`,
`targetLsn`, `targetName`; absence of all four restores to latest.
Multi-replica platforms only fire scheduled backups from the lease
holder (Postgres `pg_try_advisory_lock`).

Studio + REST API surface is identical to K8s mode — the platform
dispatches to a `BackupAdapter` based on the project's `DeploymentMode`
so the user-visible behaviour is uniform.

### BYOC

Out of scope. The operator brings their own backup story for an
externally-managed database; the platform only stores connection
credentials in vault and never holds the data.

## 7. Image upgrade

The 5 images that ship in lockstep:

| Image | Repo |
|---|---|
| `excalibase/provisioning:vX.Y.Z` | `excalibase-provisioning` |
| `excalibase/auth:vX.Y.Z` | `excalibase-auth` |
| `excalibase/excalibase-graphql:vX.Y.Z` | `excalibase-graphql` |
| `excalibase/deno-runtime:vX.Y.Z` | `excalibase-provisioning/deno-server` |
| `excalibase/excalibase-watcher-go:vX.Y.Z` | `excalibase-watcher-go` |

Bump them all together by re-running:

```bash
helm upgrade platform ./charts/platform-aio \
  --namespace excalibase-platform \
  -f values-prod.yaml \
  --set provisioning.image.tag=vX.Y.Z \
  --set auth.image.tag=vX.Y.Z \
  --set graphql.image.tag=vX.Y.Z
```

Watcher image tag is hard-coded in the provisioner (because it's deployed lazily per project) — bump `excalibase-provisioning/server-go/internal/provisioner/postgresql.go` when releasing a new watcher.

## 8. Common incidents

| Symptom | Likely cause | Fix |
|---|---|---|
| Provision returns `not enough capacity` | Cluster ran out of headroom | Add nodes, lower `CAPACITY_HEADROOM_PERCENT`, or pick a smaller tier. Detail in server log line `INFO: provision refused`. |
| CNPG cluster stuck at `WAITING_FOR_READY` | Operator panic (PodMonitor reflection bug) or PVC bind failure | `kubectl logs -n cnpg-system deployment/cnpg-controller-manager` — search for the cluster name. PodMonitor bug is fixed in v1.0.0; if you see "expected pointer, but got invalid kind", upgrade. |
| Deno runtime CrashLoopBackOff with "RUNTIME_SECRET environment variable is required" | `deno-runtime-secret` not deployed | `kubectl get secret -n excalibase-platform deno-runtime-secret`; if missing, re-run `helm upgrade`. |
| Watcher CrashLoopBackOff with "permission denied to use replication slots" | Watcher trying to use CNPG `app` role instead of `cdc_watcher` | v1.0.0 fix moves watcher deploy after role creation; upgrade. |
| Auth registration returns 503 "failed to connect to project database" | Stale auth pod with old vault path | `kubectl rollout restart -n excalibase-platform deployment/auth`. |

## 9. Static analysis (Snyk Code + SonarCloud)

**Status: 0 findings at any severity, both repos.** CI gates on
`--severity-threshold=low`, the strictest possible setting — any
new finding fails the build.

### Snyk Code

```bash
# Auth: 0 findings
snyk code test --severity-threshold=low ../excalibase-auth

# Provisioning: 0 findings
snyk code test --severity-threshold=low server-go
```

The CI step runs both. To get to zero we did three things:

1. **Production code** — fixed the original 12 medium path-traversal
   findings by adding inline `filepath.Base()` at every IO call site
   (canonical SAST sanitizer) plus a `security.SafePathComponent`
   helper for defense in depth.
2. **Production code** — renamed `"***ENCRYPTED***"` placeholder to
   `redactedSentinel = "[REDACTED]"` with a doc comment, and switched
   migration-checksum hashing from MD5 → SHA-256.
3. **Test fixtures** — moved every literal credential / username
   / token into runtime helper calls in `internal/testutil/credentials.go`
   (`FixturePassword`, `FixturePasswordHash`, `FixtureToken`,
   `FixtureSecret`). SAST tools track string literals, not function
   call results, so the helpers remove the signal at the source.
   The `.snyk` policy file is no longer needed for ignores.

### SonarCloud

`sonar-project.properties` lives at the repo root. CI uses
`SonarSource/sonarqube-scan-action@v6` (Scanner CLI 8.x) — the same
engine SonarCloud runs server-side, so local + CI + cloud scans match
exactly.

Local validation: `./scripts/sonar-local.sh` spins up SonarQube
Community 26.x in Docker and runs the same scanner against the repo.
First run takes ~3 minutes; subsequent runs reuse the container.

Required GitHub secrets:
- `SONAR_TOKEN` — generate at sonarcloud.io → My Account → Security
- `SNYK_TOKEN` — generate at app.snyk.io → Account Settings → API Token

The Sonar job pulls the Go coverage report uploaded by the `go-test`
job (`coverage.out`) so SonarCloud sees per-line coverage numbers.

## 9.1. Test validation rules

Two non-obvious gotchas have bit us in the past — agent runs and
manual cleanups have shipped subtle bugs because the default test
invocation skipped relevant code paths.

**Always run BOTH of these before claiming "tests pass":**

```bash
go vet ./... && go vet -tags=integration ./...
go test ./internal/... -short -race -count=1
```

Why: `staticcheck`, `go vet ./...`, and IDE "unused" detectors don't
see callers in `//go:build integration` files. Cleanup tooling has
deleted production code (`toFlexTime` in `internal/storage/postgres/store.go`,
May 2026) on the assumption it was unused. Confirm with both vet flags.

**Cluster-touching tests require `-tags=integration`.** The three files
in `internal/k8s/` that spin up a real cluster (`integration_test.go`,
`k3s_test.go`, `helm_k3s_test.go`) are build-tagged because testcontainers
k3s hangs for minutes if Docker is unavailable. Default `go test
./internal/...` runs fine without minikube or Docker; the tag is the
contract.

**Cleanup agents must preserve build tags.** When refactoring test files
(string-extract, helper-extract, dedup), regenerate `head -1 <file>`
against the original to confirm the `//go:build` line survives. A bulk
search-replace will quietly drop it.

## 10. Health endpoints

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /healthz` | none | Liveness — returns "ok" if process is up |
| `GET /api/capacity` | platform_admin | Cluster CPU/mem allocatable, requested, free, per-tier project headroom |
| `GET /api/admin/projects` | platform_admin | Every project across orgs with live CPU/mem |
| Grafana → Excalibase folder | grafana admin | Per-service dashboards (Auth, GraphQL, Provisioning, CNPG) with per-tenant filtering |
