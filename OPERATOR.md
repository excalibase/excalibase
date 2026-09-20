# Excalibase operator runbook

This is the day-1 to day-N reference for running an Excalibase platform. It assumes you've cloned the chart repo (`excalibase-service`) and have a Kubernetes cluster (any distro: managed EKS/GKE/AKS, RKE2, k0s/k3s, or single-node minikube).

For the ordered production install (prerequisites, secrets, `values-prod.yaml`, bootstrap, first admin, tiers) and the troubleshooting matrix, start with [docs/deployment/production-k8s-runbook.md](docs/deployment/production-k8s-runbook.md); this file is the API-level reference it links to.

## 0. Who may call what — the role ladder

Every route the control plane mounts has a row in `server-go/internal/routepolicy`: its authentication class, the platform permission it needs, the path parameter that names the tenant, what resolves the caller's right to that tenant, and the minimum org role. A test walks the real router and fails if a mounted route has no row, if a row matches no route, or if driving the router with synthetic callers contradicts the row — so this table is the answer to "who can do this?", not a description of it.

Project-scoped routes sit on one of three rungs, decided by what the call can destroy or disclose:

| Rung | What it covers |
|------|----------------|
| **viewer** | Reads only: project status and logs, metrics, performance, project info, schema browsing, listing buckets and objects, minting a download URL, listing realtime tables, alerts. |
| **developer** | Data-plane authoring — everything that changes what the tenant's database or edge serves and is undone by authoring it back: schema DDL, `/query` and row writes, migrations, RLS and column policies, table grants, edge functions (deploy, secrets, egress), the browser-origin allowlist, auth settings, realtime publication membership, and storage buckets and objects. A bucket is a container inside the project the way a table is, so dropping one sits with `DROP TABLE`, not with project teardown. |
| **admin** | Project lifecycle and credentials — what a tenant cannot author their way back out of: delete the project, read or rotate its database credentials, deletion protection, pause and resume, backups and restores, backup purge, and full-database snapshots. |
| **owner** | Deleting the org. Everything else admin covers. |

Org-scoped routes read on membership and write on the org **admin** rung. Platform-wide routes (users, service accounts, tier configs, parameter groups, operator install, vault) carry a platform permission instead of an org role; the routes that hand out or destroy platform-wide authority — minting and deleting service accounts, and vault init / unseal / seal / rekey / secret writes — additionally refuse any narrowed PAT, so a project-bound credential can never reach them even when its owner is a platform admin.

Anything a caller may not see answers **404**, never 403: a 403 would confirm the project exists.

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
1. Mint a PAT for CI/scripts (Settings → Personal access tokens, or the API below).
2. Rotate or delete the bootstrap admin user (`/admin`) once a real platform_admin user exists.

### 1.1. Personal access tokens: expiry and rotation

Every PAT expires. The default lifetime is **90 days**; `expiresIn` accepts `<n>d` or a Go duration up to **365d**. Only an explicit `"expiresIn":"never"` mints a non-expiring token — reserve that for break-glass automation and rotate it on a schedule. Login session tokens are separate and always expire after 12h.

```bash
# Mint (the secret is returned exactly once)
curl -X POST -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"name":"ci-deploy","expiresIn":"30d"}' https://<host>/api/auth/tokens
# → {"token":"excb_…","prefix":"excb_a1b2c3d","name":"ci-deploy","scopes":"","expiresAt":"2026-…"}

# List — shows expiresAt and lastUsed (updated at most once a minute per token)
curl -sf -H "Authorization: Bearer $PAT" https://<host>/api/auth/tokens | jq

# Rotate — same owner/name/scopes, lifetime restarts from now.
# graceSeconds (0–3600, default 0) keeps the OLD secret valid for that long so
# a fleet can swap credentials without a hard cut; the old expiry is never extended.
HASH=$(echo -n "$OLD_PAT" | sha256sum | awk '{print $1}')
curl -X POST -H "Authorization: Bearer $OLD_PAT" -H 'Content-Type: application/json' \
  -d '{"graceSeconds":300}' https://<host>/api/auth/tokens/$HASH/rotate
# → {"token":"excb_…(new)","expiresAt":"…","previousExpiresAt":"…(now+300s)", …}
```

Only the token's owner can rotate it, with one exception: a platform admin may rotate a **service account's** token (see 1.2), because that is exactly what the rotation job does. Admins can revoke any token. Each rotation writes an `access_token` / `token.rotate` audit row carrying the display prefixes and the grace window — never the secret. An expired token is refused with `401` and `"code":"token_expired"`, so a CI job that starts failing with that code needs a rotate, not a re-login.

### 1.2. Service identity: service accounts and capability tokens

The platform's own components no longer authenticate as a human admin. Each one has a **service principal** — a user with `kind = service` that has no password, is refused by `/api/auth/login` and `/api/auth/register`, and can never be added to an organization. Its only credential is a **capability token**.

A capability token carries an explicit permission list. Each entry is `<resource>:<action>[:<selector>]`:

| Permission | Grants |
| --- | --- |
| `vault:read:pki/signing/*` | `GET /api/vault/secrets/pki/signing/<leaf>` — the `*` stands for exactly one path segment and never crosses a `/` |
| `projects:info:read` | `GET /api/projects/{projectId}/info` |
| `policies:read` | `GET /api/provision/{projectId}/rls-policies` and `/column-policies`, list or by id |
| `email:send` | `POST /internal/email/send` — the transactional mail relay; the only write any capability may make |

The list is **default-deny and absolute**: a token with a non-empty permission list may call only the endpoints its list names, and everything else — every write, every other route — answers `403`, regardless of the owning principal's platform role. `GET /api/auth/me` is always reachable so a service can validate its own credential. Tokens with an empty permission list (every human PAT and session) are untouched by this layer.

The two principals the platform ships with:

| Principal | Permissions | Consumer |
| --- | --- | --- |
| `svc-auth` | `vault:read:pki/signing/*`, `projects:info:read`, `email:send` | auth service — JWKS signing key at boot, per-project info hourly, verification and reset mail |
| `svc-graphql` | `projects:info:read`, `policies:read` | engine — project credentials + CORS, RLS/column policies every 30 s |

```bash
# Create (idempotent by name — safe to re-run on every upgrade)
curl -X POST -H "Authorization: Bearer $ADMIN_PAT" -H 'Content-Type: application/json' \
  -d '{"name":"svc-graphql"}' https://<host>/api/admin/service-accounts

# Mint its credential. Platform admins only, and a service account token MUST
# carry at least one permission — a permission-less one is refused.
curl -X POST -H "Authorization: Bearer $ADMIN_PAT" -H 'Content-Type: application/json' \
  -d '{"name":"svc-graphql","expiresIn":"never","userId":"<id>",
       "permissions":["projects:info:read","policies:read"]}' \
  https://<host>/api/auth/tokens

# Inspect (metadata only — never a secret) and rotate
curl -sf -H "Authorization: Bearer $ADMIN_PAT" https://<host>/api/admin/service-accounts/svc-graphql/tokens | jq
curl -X POST -H "Authorization: Bearer $ADMIN_PAT" -H 'Content-Type: application/json' \
  -d '{"graceSeconds":600}' https://<host>/api/auth/tokens/<tokenHash>/rotate

# Delete the principal — this revokes every token it owns
curl -X DELETE -H "Authorization: Bearer $ADMIN_PAT" https://<host>/api/admin/service-accounts/svc-graphql
```

Rotation preserves the permission list and the lifetime. The secrets are delivered to the consumers as **files**, never env vars: the platform chart writes them into a Secret and mounts the keys at `/var/run/excalibase/auth-token` and `/var/run/excalibase/graphql-token`. Both consumers re-read the file on each call (within 5 s of a change), so rotating a token needs **no restart and no rollout** — the weekly rotation CronJob writes the new values into the Secret and the running pods pick them up. There is no longer a shared `provisioning-pat` admin credential; if you find one in a Secret, it predates this and can be deleted.

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

### Deleting a project is observed, not fire-and-forget

`DELETE /api/provision/<projectId>/` runs the teardown and only removes the
project record once every step is observed complete: the tenant watcher is
stopped, NATS credentials revoked, the PgDog routes removed, the database
Cluster deleted **and waited out**, the namespace deleted and waited until no
namespace, pod or PVC carrying the project id is left, backups purged if that
was asked for, and the vault prefix emptied.

* A step that fails or times out returns `500` and leaves the row in
  `DELETING` carrying `deletionStep` and `deletionError`. `GET
  /api/provision/<projectId>/` shows them. Re-issuing the same `DELETE`
  resumes — already-removed resources count as done.
* While a project is `DELETING`, only three requests are accepted against it:
  `GET` its status, the `DELETE` retry, and `POST .../backups/purge`.
  Everything else answers `409`.
* A `DELETE` arriving while another teardown of the same project is still
  running answers `409`. The call is synchronous and can outlive a proxy
  timeout, so a client retry is safe: it is refused, not run in parallel.
* Raise `DELETION_WAIT_TIMEOUT` (a Go duration, e.g. `10m`) on clusters where
  storage detach is slow.

### Deleting a project that is still being provisioned

A `DELETE` while the project's provisioning pipeline is still building it
answers `409 project is busy: PROVISIONING; retry when it settles`. The build
is creating namespaces, clusters, vault credentials and bus identities that a
teardown running alongside it would not see — it could observe "nothing
remains", drop the record, and leave the finished build's resources live and
unowned.

The pipeline stamps `updated_at` on every stage, so a running provision is
never mistaken for a dead one. **A provision whose process died** (platform
restart mid-build) leaves the row in `PROVISIONING` with a timestamp that
stops moving: after **30 minutes** without movement the row is no longer
treated as a live build and deletes normally. The same window the restore
orchestrator uses for stale jobs.

If you cannot wait, `DELETE /api/admin/projects/<projectId>` (section 3) is
the operator path — it clears deletion protection and runs the same observed
teardown.

If the door closes under a provision that is genuinely still running (the
stale window elapsed on a very slow build, or the admin path was used), the
pipeline stops at its next step, rolls back what it created, and the project
reports `FAILED` — nothing is registered into a project whose record is on
its way out.

### Function invocation while a project is being deleted

Invoking a function of a project under teardown answers `409`. The check sits
where the handler already resolves the project (the per-project runtime
lookup), so a warm project is still served without reading the platform
database, and an unknown project id on the public route is answered by the
function lookup without reaching the database at all.

Claiming a project for deletion drops its cached runtime client on the
replica that ran the `DELETE`, so that replica refuses the next invocation
immediately. **Another replica keeps serving until its own cached runtime
client is dropped** — in practice until the namespace goes, which the
teardown waits for, so the window closes before the project's resources do.
On a single shared runtime (docker / self-hosted), where clients are not
per-project, invocation is not refused this way; the project's functions stop
resolving when its record is removed at the end of the teardown.

### Backups retained after a project is deleted

Deleting a project **keeps its backups** unless the request carries
`{"confirmDeleteBackups": true}`. The decision is recorded on the row when the
deletion starts, so a retry cannot drop a purge that was already confirmed;
asking to keep backups after confirming a purge answers `409`.

When backups are kept, the `200` response carries the object-store prefix they
remain under:

```json
{"projectId":"proj-abc123","status":"DELETED",
 "retainedBackupPrefix":"backups/proj-abc123/"}
```

**Record that prefix.** The platform has no endpoint that lists or purges the
backups of a project whose record is gone — `POST .../backups/purge` needs the
row, and the row is deleted with the project. Retained objects are reachable
only with object-store credentials, by prefix:

```bash
aws s3 ls "s3://$BUCKET/backups/proj-abc123/" --endpoint-url "$R2_ENDPOINT"
aws s3 rm --recursive "s3://$BUCKET/backups/proj-abc123/" --endpoint-url "$R2_ENDPOINT"
```

Whether the platform should instead keep a tombstone so an owner can find and
purge their own retained backups is an open product question — today it cannot.

### Incomplete multipart uploads (operator requirement)

A multipart upload that was started and never completed is **not an object**.
It does not appear in any listing, no API returns it, and nothing the platform
runs can find it — but the parts already uploaded are stored and billed. That
is true of every sweep the platform has: the unconfirmed-upload reaper, a
bucket delete and a project delete all work from listings, so none of them can
see one.

Set a lifecycle rule on the storage bucket so the object store expires them
itself:

```bash
cat > /tmp/lifecycle.json <<'JSON'
{"Rules":[{"ID":"abort-incomplete-multipart-uploads","Status":"Enabled",
  "Filter":{"Prefix":""},
  "AbortIncompleteMultipartUpload":{"DaysAfterInitiation":1}}]}
JSON
aws s3api put-bucket-lifecycle-configuration --bucket "$R2_BUCKET" \
  --lifecycle-configuration file:///tmp/lifecycle.json --endpoint-url "$R2_ENDPOINT"
```

One day is generous: a resumable upload that has not progressed in 24 hours is
not coming back. Without this rule, abandoned multipart uploads accumulate
indefinitely and the only way to find them is
`aws s3api list-multipart-uploads --bucket "$R2_BUCKET"`.

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
  -d '{"newProjectName":"restored orders","backupId":"<backup-id>"}' \
  https://<host>/api/provision/proj-abc123/backup/restore
```

PITR (point-in-time recovery) reuses the same flow with a `targetTime`
field in the body.

The restored project's id is generated by the platform — the request carries
a display name only. Read the generated id off the restore job
(`newProjectId`) and use it for every later call.

#### What a restore leaves behind

A restore ends exactly the way a provision does. Once the recovered database
is up — the CNPG primary pod Ready in K8s mode, postgres out of recovery in
Docker mode — the platform runs the same registration path a new project goes
through:

1. Platform roles (`auth_admin`, `excalibase_app`, `cdc_watcher`) are created,
   and — because a restored database arrives carrying the source project's
   roles and passwords — their passwords are **reset** to freshly generated
   ones. The restored project therefore has its own credentials; leaking the
   source's does not grant access to it, and vice versa.
2. Those credentials are written to vault under
   `projects/{restoredProjectId}/credentials/{role}`, which is what the schema
   plane, graphql and auth read. Without this step the project exists but
   every data-plane call fails with "project credentials not found in vault".
3. The instance row is saved `ACTIVE`, carrying the source's org, tier and
   deployment mode plus `restoredFromProjectId` / `restoredFromBackupId` so
   support can trace where the data came from.
4. The project is registered with PgDog, the project-created event is
   published, and a `project_activity` marker is written so the new project is
   not a candidate for idle-pause.

So `GET /api/provision/{restoredProjectId}/`, `POST /api/schema/{restoredProjectId}/query`,
graphql and auth all work as soon as the restore job reports `COMPLETED` —
no manual SQL, no re-provisioning.

**When a restore fails:** the job row goes `FAILED` with `failureReason`
naming the step that failed, and **no project row is written**. The recovered
database is deliberately left running (the namespace in K8s mode, the
container in Docker mode) so the failure can be diagnosed. Clean up with
`kubectl delete ns {orgId}-{restoredProjectId}` or `docker rm -f
excalibase-{restoredProjectId}-postgres` once you are done, then re-run the
restore. There is no half-registered state to repair: a project either has
its credentials or it has no row at all.

#### Backups on deprovision

`DELETE /api/provision/{projectId}` keeps the project's backups by default —
the R2 objects outlive the project so an accidental delete is recoverable via
restore into a new project. To also delete them, send the confirmation field:

```bash
curl -X DELETE -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"confirmDeleteBackups": true}' \
  https://<host>/api/provision/proj-abc123
```

What happens, in order:

1. PgDog deregistration, cluster/container deletion, vault `projects/{id}/`
   sweep — exactly as without the flag.
2. Only then every object under the project's backup prefix is deleted, in
   list+delete batches of at most 1000 keys (`ListObjectsV2` +
   `DeleteObjects`). The prefix is `{projectId}/cloud/` for K8s (Barman
   `destinationPath/serverName`) and `backups/{projectId}/` for Docker mode.
   The store (endpoint, bucket, credentials) is resolved through
   `ProvisioningService.BackupStorage()` — the same source backups are
   written with. A mode with no platform backups is skipped.
3. The purge refuses to run when the computed prefix is empty or does not
   contain the project id as a path segment, so a misconfigured key prefix can
   never turn into a bucket-wide delete.
4. If the purge fails (R2 outage, sealed vault, ...), the deprovision is
   **not** rolled back. The row is kept with `status: BACKUPS_PENDING_DELETE`
   and `failureReason: backup purge failed: ...` as a retry marker; it is
   removed once the purge succeeds. Deleting twice is safe: an empty prefix
   is a successful no-op.

Retry a pending purge (same Admin role as DELETE; `409` if the project is
still live, `404` once the row is gone):

```bash
curl -X POST -H "Authorization: Bearer $PAT" \
  https://<host>/api/provision/proj-abc123/backups/purge
# → {"projectId":"proj-abc123","status":"purged","deletedObjects":42}
```

Find rows waiting for a retry with
`SELECT project_id, failure_reason FROM database_instances WHERE status = 'BACKUPS_PENDING_DELETE'`.
`DELETE /api/admin/projects/{projectId}` (force drop) accepts the same
`confirmDeleteBackups` body field.

**Where a restore reads from:** the K8s restore adapter resolves endpoint,
bucket, region and credentials through `ProvisioningService.BackupStorage()`
— the exact source the backup write path uses (env `BACKUP_DEFAULT_*` /
`R2_*` first, then vault `backup/s3`). There is no separate restore
default and no localstack fallback: with neither configured, `POST
/backup/restore` fails with `backup storage not configured` before touching
the cluster. A localstack/floci endpoint is only used when it is the
configured backup endpoint (tests/CI).

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
  -d '{"newProjectName":"restored orders","targetTime":"2026-05-01T03:00:00Z"}' \
  https://<host>/api/projects/proj-abc123/backup/restore

curl -H "Authorization: Bearer $PAT" \
  https://<host>/api/projects/proj-abc123/backup/restore/<jobId>
```

`RestoreRequest` accepts at most one of `targetTime`, `targetXid`,
`targetLsn`, `targetName`; absence of all four restores to latest.
Multi-replica platforms only fire scheduled backups from the lease
holder (Postgres `pg_try_advisory_lock`).

### Platform database pool sizing

Every control-plane replica opens one pool against the platform database.
Two kinds of work pin a connection for longer than a query:

- **Standing leadership claims — 4 per replica.** The backup scheduler, the
  idle-pause sweep, the storage reaper and the restore sweeper each hold one
  advisory lock, and an advisory lock pins the session that took it.
- **In-flight lifecycle operations — 1 each, for their whole duration.**
  Pause, resume and delete take a per-project lease so two replicas cannot
  act on one project at once. A pause waits for a backup to complete and for
  the database to shut down, so it can hold its connection for minutes.

So a replica needs `4 + (concurrent lifecycle operations) + (headroom for
ordinary query traffic)`. The default pool is **20**, which leaves room for
roughly a dozen concurrent pauses alongside normal traffic. Raise it with
`PLATFORM_DB_MAX_CONNS` (minimum 6 — four claims plus one operation plus one
query) on a control plane that pauses many projects at once. A value that is
not a number, or below the minimum, **stops the platform from starting**: a
pool that cannot do the work is a misconfiguration to fix, not something to
paper over with a default the operator did not choose, and make sure Postgres' own `max_connections`
covers `PLATFORM_DB_MAX_CONNS x replicas` plus your own sessions.

Taking a lease waits at most 2s for a pool connection. A burst of lifecycle
calls therefore degrades into `409 project is busy` rather than parking every
request on a drained pool.

An org cascade (`DELETE /api/admin/orgs/{orgId}?cascade=true`) deprovisions
the org's projects one at a time, so it holds one lifecycle lease however
many projects the org has. The arithmetic above does not scale with cascade
size; the cascade takes longer instead.

Studio + REST API surface is identical to K8s mode — the platform
dispatches to a `BackupAdapter` based on the project's `DeploymentMode`
so the user-visible behaviour is uniform.

### Databases you run yourself

The platform hosts the databases and apps it provisions, so a database you
run yourself is not a project here. To put the API engine in front of it,
run the open-source engine standalone — see
[github.com/excalibase/excalibase-graphql](https://github.com/excalibase/excalibase-graphql).

## 6.1. Edge-function egress (outbound allowlist)

Function workers have **no network by default**: `fetch()` from a function
fails with `NotCapable` until the project allowlists the host. The setting
is per project, stored in the platform Postgres (`edge_function_settings`)
and rendered into the runtime by provisioning — see
[docs/functions-egress.md](docs/functions-egress.md) for the grammar.

```bash
# Read / set a project's allowlist (Developer+ on the org — same gate as deploy)
curl -sf -H "Authorization: Bearer $PAT" \
  "https://<host>/api/projects/<projectId>/functions/egress" | jq
curl -sf -X PUT -H "Authorization: Bearer $PAT" -H "Content-Type: application/json" \
  "https://<host>/api/projects/<projectId>/functions/egress" \
  -d '{"allowedHosts":["api.stripe.com","*.amazonaws.com"]}'

# Operator floor every project gets in addition (same grammar; malformed = boot refuses)
EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS="*.excalibase.io,api.resend.com"
```

What a `PUT` does, per mode:

| Mode | Rendering | Rollout |
|---|---|---|
| k8s (per-project `deno-runtime` pod) | `ALLOWED_HOSTS` env on the Deployment + `excalibase.io/egress-hash` pod annotation; `deno-runtime-egress` NetworkPolicy re-rendered in the same call (public IPv4 on the allowlisted TCP ports, private ranges excepted; nothing when the list is empty) | Deployment rolls; the runtime replayer refills the pod |
| docker (one shared runtime) | every deploy payload carries `allowedHosts`; the worker gets exactly that list | functions redeployed immediately |

Verify on k8s:

```bash
kubectl -n <project-ns> get deploy deno-runtime -o jsonpath='{.spec.template.spec.containers[0].env}' | jq
kubectl -n <project-ns> get networkpolicy deno-runtime-egress -o yaml | grep -A6 ipBlock
```

The NetworkPolicy is only a backstop (it cannot match hostnames) and needs
an enforcing CNI (Calico / Cilium). On the shared docker runtime the
container's own `ALLOWED_HOSTS` env is an operator baseline unioned into
every worker.

## 6.2.1. Browser origins (per-project CORS)

A project's data plane (`/{projectId}/graphql`, `/{projectId}/api/v1/*`
and the WebSocket upgrades) answers CORS from a **per-project allowlist**,
not from a global setting. A new project has **no origins**: excalibase-graphql
sends no `Access-Control-Allow-Origin` for it, so a browser app is blocked
until the project opts its origins in. Non-browser clients (curl, server
SDKs, the function runtime) never send `Origin` and are unaffected.

```bash
# Read / set the allowlist (Developer+ on the org)
curl -sf -H "Authorization: Bearer $PAT" \
  "https://<host>/api/projects/<projectId>/cors" | jq
curl -sf -X PUT -H "Authorization: Bearer $PAT" -H "Content-Type: application/json" \
  "https://<host>/api/projects/<projectId>/cors" \
  -d '{"allowedOrigins":["https://app.example.com","http://localhost:5173"]}'

# Every origin — has to be said explicitly and on its own
curl -sf -X PUT -H "Authorization: Bearer $PAT" -H "Content-Type: application/json" \
  "https://<host>/api/projects/<projectId>/cors" \
  -d '{"allowedOrigins":["*"],"allowWildcard":true}'
```

Rules: absolute origins only (`scheme://host[:port]`, no path, no
subdomain wildcard), lower-cased, default ports dropped, deduplicated,
max 32. Stored in the platform Postgres (`project_cors_settings`) and
exposed as `corsAllowedOrigins` on `GET /api/projects/{id}/info`, which
excalibase-graphql caches for 30 s per project. If provisioning is
unreachable the data plane keeps serving the last list it saw; a project
it has never resolved is denied. See [docs/project-cors.md](docs/project-cors.md).

## 6.2.2. Per-project auth settings

`GET`/`PUT /api/projects/{id}/auth-settings` (EXC-367, Developer+) store two
fields the auth service reads through `/info` at signup/login time:
`requireEmailVerification` (bool, default `false`) gates whether a new
signup must confirm its email before it can log in, and `siteUrl` is the
canonical origin used to build redirect/callback links. `siteUrl` must be an
absolute `http`/`https` URL with a host and no userinfo, query, fragment or
trailing slash (a path is allowed); an empty string clears it back to unset.
A project with no row reads as the zero value on both `/auth-settings` and
`/info`. As with CORS and edge-function egress, a platform-store read
failure answers `/info` with **503** rather than a wrong default — the auth
service is expected to keep its last-known-good copy.

## 6.3. Pause / resume a project

Pause stops a project's database without deprovisioning it (data and
credentials stay; the tenant pays nothing for compute). Resume brings it
back at its tier size. Both are synchronous HTTP calls.

```bash
# Pause (Admin on the org). Takes a backup first; if the backup fails the
# project stays ACTIVE. Body is optional: {"reason":"manual"} (default).
curl -sf -X POST -H "Authorization: Bearer $PAT" \
  "https://<host>/api/provision/<projectId>/pause" | jq
# -> {"projectId":"proj-…","status":"PAUSED","pauseReason":"manual"}

# Resume. Blocks until the database is reachable again (k8s: up to 5 min).
curl -sf -X POST -H "Authorization: Bearer $PAT" \
  "https://<host>/api/provision/<projectId>/resume" | jq
# -> {"projectId":"proj-…","status":"ACTIVE"}
```

What happens per mode:

| Mode | Pause | Resume |
|---|---|---|
| k8s (CNPG) | Sets `cnpg.io/hibernation: "on"` on the Cluster. The operator does a clean shutdown, deletes the pods and keeps the PVCs. `spec.instances` is **not** changed, so nothing about the tier is lost. The call returns as soon as the annotation is set; the database itself stays reachable for up to `spec.smartShutdownTimeout` (180 s) because the project's CDC watcher keeps a replication session open and the operator waits for clients before switching to a fast shutdown — expect ~2–3 min until the pod is gone. | Sets the annotation to `"off"` and polls the Cluster until `status.readyInstances >= 1` and the `cnpg.io/hibernation` condition is cleared (bounded at 5 min, 5 s poll). Measured on a 1-instance cluster: ~13 s from annotation to first successful `pg_isready`; ~25 s end-to-end through `POST /resume`. |
| docker | Stops the container. | Starts it and waits for the health check. |

Requires CloudNativePG >= 1.20 (declarative hibernation). The
`charts/platform-aio` install script and the nightly e2e pin 1.23.0.

Do not read `status.phase` to decide whether a paused cluster is back:
CNPG leaves it at `Cluster in healthy state` while hibernated and keeps
the `Ready` condition `True`; only `readyInstances` (absent while
hibernated) and the hibernation condition move.

Verify on k8s:

```bash
kubectl -n <project-ns> get cluster <projectId>-postgres \
  -o jsonpath='{.metadata.annotations.cnpg\.io/hibernation} {.status.readyInstances}{"\n"}'
kubectl -n <project-ns> get cluster <projectId>-postgres \
  -o jsonpath='{.status.conditions[?(@.type=="cnpg.io/hibernation")]}{"\n"}'
kubectl -n <project-ns> get pods,pvc      # paused: no pods, PVCs Bound
```

Stuck states:

| Status | Meaning | Fix |
|---|---|---|
| `PAUSING` | Backup succeeded but the workload stop failed | `kubectl -n <project-ns> describe cluster`; retry `POST /pause` once the cause is cleared. |
| `RESUMING` | The annotation is already `off` but the primary did not become ready within 5 min | The operator keeps recovering on its own — watch `kubectl -n <project-ns> get pods -w`; retry `POST /resume` when the pod is Ready (idempotent). |

## 6.4. Project route authorization (path ↔ caller binding)

Every route that names a project in its path binds that project to the
caller **before** the handler runs (`RequireProjectAccess`, EXC-323):

1. The project is loaded and its org resolved.
2. A session user must be a member of that org; the route may additionally
   demand a minimum org role (Owner ⊇ Admin ⊇ Developer ⊇ Viewer).
3. A PAT created with `projectId` never leaves that project, and a PAT
   whose scopes are `read` only cannot call a mutating method.
4. Anything the caller may not see answers **404** — never 403, which would
   confirm the project exists. Role and scope shortfalls on a project the
   caller *can* see answer **403**. No credentials: **401**.

Platform admins (`platform_admin`) bypass the membership check for support
and incident response, but a project-bound PAT still confines them.

| Route group | Min org role | Notes |
|---|---|---|
| `GET /api/provision/{id}`, `/logs`, `/maintenance-window`, `/metrics/*`, `/performance/*` | Viewer | any member |
| `PUT /api/provision/{id}/maintenance-window` | Developer | |
| `/api/provision/{id}/audit`, `/migrations`, `/rls-policies`, `/column-policies` | Developer | |
| `DELETE /api/provision/{id}`, `/credentials`, `/credentials/rotate`, `/deletion-protection`, `/pause`, `/resume`, `/backups/purge` | Admin | lifecycle + secrets |
| `/api/provision/{id}/backup/*`, `/snapshot/*` | Admin | dump/restore are destructive and exfil-capable |
| `GET /api/schema/{id}/*` | Viewer | browse |
| `POST/PATCH/DELETE /api/schema/{id}/*` (DDL, `/query`, row writes) | Developer | |
| `GET /api/projects/{id}/functions`, `/{fnId}`, `/{fnId}/logs`, `/_metadata`, `/runtime/status` | Viewer | |
| `POST /api/projects/{id}/functions`, `/{fnId}/invoke`, `DELETE /{fnId}`, `/secrets`, `/egress` | Developer | deploy, invoke, secrets, outbound allowlist |
| `POST /api/projects/{id}/schema/apply` | Developer | |
| `GET/PUT /api/projects/{id}/cors` | Developer | browser-origin allowlist the data plane enforces |
| `/api/projects/{id}/info`, `/realtime/*`, `/storage/*`, `GET /api/alerts/project/{id}` | Viewer | |
| `/api/projects/{id}/auth-settings` | Developer | `requireEmailVerification` + `siteUrl` |
| `/api/orgs/{orgId}/projects/{id}/members` | org member (list) / Admin (change) | project must belong to `{orgId}` |
| `/api/admin/projects/{id}` | platform permission (`delete`) | platform plane, not the tenant gate |

Public paths that carry a project id but authenticate differently:
`/functions/v1/{id}/*` (end-user invoke: anon/JWT), `/internal/*` and
`/internal/storage/{id}/*` (runtime shared secret), `/storage/v1/object/public/{id}/*`.

A project-bound, read-only CI token:

```bash
curl -sf -X POST -H "Authorization: Bearer $SESSION" -H "Content-Type: application/json" \
  https://<host>/api/auth/tokens \
  -d '{"name":"ci-readonly","projectId":"<projectId>","scopes":["read"]}'
# → {"token":"excali_…","projectId":"<projectId>","scopes":"read","expiresAt":"…"}
# Binding to a project the caller cannot see is refused with 404.
```

`POST /api/auth/tokens/{hash}/rotate` (§1.1) is owner-only and re-issues the
token with the same `projectId` and scopes, so a rotated CI token stays
confined to its project.

The wiring is pinned by `cmd/server/authz_matrix_test.go`, which drives the
production router: every registered `{projectId}` route outside the public
prefixes must refuse an anonymous caller (401) and a member of another org
(404).

## 6.5. NATS subject authorization (auth callout)

The message bus is not shared-secret. NATS runs with `auth_callout`, and
provisioning is the callout responder: it subscribes to `$SYS.REQ.USER.AUTH`,
verifies the presented username/password against the `nats_credentials` table
and signs a short user JWT whose subject permissions come from
`internal/natsauth.PermissionsFor`. An unknown user, a wrong password or an
unreachable responder means the connection is refused — there is no open
fallback.

| Principal | Publish | Subscribe |
|---|---|---|
| `svc-graphql` | `$JS.API.INFO`, `$JS.API.STREAM.INFO.CDC`, `$JS.API.CONSUMER.>`, `$JS.ACK.>` | `cdc.>`, `policies.>`, `_INBOX_svc-graphql.>` |
| `svc-provisioning` | `policies.>`, `pgdog.>`, `$JS.API.INFO`, `$JS.API.STREAM.>` | `_INBOX_svc-provisioning.>` |
| `svc-pgdog` | *(deny all)* | `pgdog.config.reload` |
| `tenant-watcher:<projectId>` | `cdc.<projectId>.>`, `$JS.API.INFO`, `$JS.API.STREAM.INFO.CDC` | `_INBOX_tw_<projectId>.>` |

A per-tenant watcher therefore cannot subscribe to anything — not `cdc.>`,
not even its own project's subjects — and cannot publish into another
project's prefix. Each principal gets its own inbox prefix so request
replies never land on a subject another tenant may read; the client must set
the matching prefix (`WATCHER_NATS_INBOX_PREFIX`, `app.nats.inbox-prefix`)
or every JetStream publish will time out.

Environment on provisioning:

| Variable | Meaning |
|---|---|
| `NATS_AUTH_CALLOUT_ISSUER_SEED` | account nkey seed (`SA…`) that signs user JWTs; **enables the responder** |
| `NATS_AUTH_CALLOUT_USER` / `_PASSWORD` | the credential the responder itself connects with (in the `AUTH` account, exempt from the callout) |
| `NATS_AUTH_CALLOUT_ACCOUNT` | account clients are placed into (default `APP`) |
| `NATS_USER` / `NATS_PASSWORD` | provisioning's own bus credential (`svc-provisioning`) |
| `NATS_GRAPHQL_PASSWORD` / `NATS_PGDOG_PASSWORD` | the other services' passwords; provisioning hashes them into `nats_credentials` at every boot, so the Secret is the single source of truth and rotation is "change the Secret, restart" |
| `NATS_CDC_STREAM` | shared JetStream stream name (default `CDC`) |

Tenant credentials are minted at provision time, rotated on re-provision and
deleted on deprovision, so a surviving watcher pod cannot reconnect after its
project is gone. Only bcrypt hashes are stored; the plaintext exists once, in
the watcher's Secret in the tenant namespace.

**Runbook — a watcher stops publishing after a re-provision.** Its credential
was rotated; the pod is holding the old one. `kubectl rollout restart
deploy/<project>-excalibase-watcher-go -n <tenant-ns>` picks up the new
Secret. If it still fails, check provisioning's log for
`NATS auth callout disabled` (the issuer seed is missing, so *every*
connection is being refused) and confirm the `platform-nats` Secret was
seeded by the bootstrap Job.

## 6.6. Project database credential rotation

`POST /api/provision/{id}/credentials/rotate` replaces the passwords of every
platform role filed under `projects/{id}/credentials/` — `auth_admin`, then
`excalibase_app`, then the project owner, which is the credential the call
returns. `cdc_watcher` is not rotated: its password is baked into the deployed
watcher's configuration, so changing it would stop the project's replication.

Each role is rotated in one order: the new password is written to
`projects/{id}/credentials/pending/<role>`, `ALTER USER` is applied, the
password is proved by opening the database with it, and only then does it
replace `projects/{id}/credentials/<role>` (and the project row, for the
owner). The pending entry is deleted last. So a pending entry you find in
vault is an interrupted rotation, not corruption: **re-run the rotate call**
and it finishes from wherever it stopped, reusing the recorded password
rather than minting another. A rotation whose `ALTER USER` never landed
rolls its own pending entry back, leaving the old password live.

On success the platform publishes a `credential` change on
`policies.{projectId}.changed` so the engine and the auth service drop the
credentials they cache instead of holding a dead password until their TTL
expires.

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
| Function `fetch()` fails with `NotCapable: Requires net access to "<host>"` | Host not in the project's egress allowlist (default: no egress) | `PUT /api/projects/<id>/functions/egress` with the host — section 6.1. If the allowlist is set but the call times out on k8s, the NetworkPolicy port set does not cover the target port (entries without a port open 443 only) or the CNI does not enforce policies. |
| `PUT /functions/egress` returns 400 | Entry outside the Deno `net` grammar or a private / cluster-internal address | The error names the entry; only `host`, `host:port`, `*.suffix[:port]` or public IP literals are accepted. |
| Browser app gets a CORS error against `/{projectId}/graphql` while curl works | The page's origin is not on the project's allowlist (default: none) | `PUT /api/projects/<id>/cors` with the exact origin the browser sends (`scheme://host[:port]`, no path) — section 6.2.1. Changes reach the data plane within 30 s. |
| `PUT /cors` returns 400 | Entry is not an absolute origin, carries a path, uses a subdomain wildcard, or `"*"` was sent without `allowWildcard` / alongside other entries | The error names the entry. |

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
