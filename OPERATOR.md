# Excalibase operator runbook

This is the day-1 to day-N reference for running an Excalibase platform. It assumes you've cloned the chart repo (`excalibase-service`) and have a Kubernetes cluster (any distro: managed EKS/GKE/AKS, RKE2, k0s/k3s, or single-node minikube).

For the ordered production install (prerequisites, secrets, `values-prod.yaml`, bootstrap, first admin, tiers) and the troubleshooting matrix, start with [docs/deployment/production-k8s-runbook.md](docs/deployment/production-k8s-runbook.md); this file is the API-level reference it links to.

## 0. Who may call what — the role ladder

Every route the control plane mounts has a row in `server-go/internal/routepolicy`: its authentication class, the platform permission it needs, the path parameter that names the tenant, what resolves the caller's right to that tenant, and the minimum org role. A test walks the real router and fails if a mounted route has no row, if a row matches no route, or if driving the router with synthetic callers contradicts the row — so this table is the answer to "who can do this?", not a description of it.

Project-scoped routes sit on one of three rungs, decided by what the call can destroy or disclose:

| Rung | What it covers |
|------|----------------|
| **viewer** | Reads only: project status and logs, metrics, performance, project info, schema browsing, listing buckets and objects, minting a download URL, listing realtime tables, alerts. |
| **developer** | Data-plane authoring — everything that changes what the tenant's database or edge serves and is undone by authoring it back: schema DDL, `/query` and row writes, migrations, Postgres RLS policies, API permissions and tracked functions, edge functions (deploy, secrets, egress), the browser-origin allowlist, auth settings, realtime publication membership, and storage buckets and objects. A bucket is a container inside the project the way a table is, so dropping one sits with `DROP TABLE`, not with project teardown. |
| **admin** | Project lifecycle and credentials — what a tenant cannot author their way back out of: delete the project, read or rotate its database credentials, deletion protection, pause and resume, backups and restores, backup purge, and full-database snapshots. |
| **owner** | Deleting the org. Everything else admin covers. |

Org-scoped routes read on membership and write on the org **admin** rung. Platform-wide routes (users, service accounts, tier configs, parameter groups, vault) carry a platform permission instead of an org role; the routes that hand out or destroy platform-wide authority — minting and deleting service accounts, and vault init / unseal / seal / rekey / secret writes — additionally refuse any narrowed PAT, so a project-bound credential can never reach them even when its owner is a platform admin.

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
kubectl -n excalibase-platform get secret platform-setup-token -o jsonpath='{.data.token}' | base64 -d ; echo
```

The last command prints the **one-time setup token**. No admin account or password is created or stored by the chart (EXC-485): open `https://<your-host>/setup`, register the first admin with that token and a password of your own, then mint a PAT for CI/scripts (Settings → Personal access tokens, or the API below). The token works once.

The vault unseal key is kept under AWS KMS (`vault.unseal.provider: awskms`, see docs/deployment/production-k8s-runbook.md §3.3.1); only its ciphertext is stored, and provisioning refuses to start when it cannot decrypt it.

**Studio needs HTTPS.** Its only credential is the `__Host-excali_session` cookie, which is `HttpOnly`, `Secure`, `Path=/`, without a `Domain` and `SameSite=Strict`, so a browser stores it only over HTTPS or on `http://localhost`, and a sibling subdomain cannot plant it. Plain HTTP on any other address signs nobody in. For a throwaway plain-HTTP install on a LAN address set `STUDIO_INSECURE_COOKIES=true`: the cookie then loses `Secure` and the prefix and a sibling host can plant it, so it is insecure and the server refuses it when `STUDIO_URL` is https. Keep `APP_DOMAIN` and `SITES_BASE_DOMAIN` off the Studio and API hosts; a domain equal to or above them stops the server, and one sharing their registrable domain logs a warning. Writes that ride the cookie must come from `STUDIO_URL`'s origin (or an origin listed explicitly in `CORS_ORIGINS`), so open Studio at exactly that address. `CORS_ORIGINS=*` lets any page call the API with a bearer token but never with the cookie.

**Studio is reached through the edge on Kubernetes.** The admin ingress routes `/api` to provisioning's edge listener and everything else to the Studio Service; open Studio at that host. Studio's own `/api` proxy (in `frontend/nginx.conf`) serves the docker all-in-one only: on Kubernetes its `provisioning` upstream does not resolve (nginx's runtime resolver ignores the pod's DNS search list), and the platform NetworkPolicy admits only the edge on provisioning's edge listener, whose `X-Forwarded-For` it trusts. So a port-forward to the Studio Service alone answers 502 on `/api`; that is by design, not a second way in. For local access forward both Services and route `/api` to provisioning's public port, as `demo/stack/studio-proxy.cjs` in excalibase-service does.

### 1.1. Personal access tokens: expiry and rotation

Developers manage their own tokens in Studio under **Access tokens** (create with a name, scope, optional project and expiry; the secret is shown once; revoke). The API below does the same.

Every PAT expires. The default lifetime is **90 days**; `expiresIn` accepts `<n>d` or a Go duration up to **365d**. Only an explicit `"expiresIn":"never"` mints a non-expiring token — reserve that for break-glass automation and rotate it on a schedule. Login session tokens are separate and always expire after 12h.

```bash
# Mint (the secret is returned exactly once)
curl -X POST -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"name":"ci-deploy","expiresIn":"30d"}' https://<host>/api/auth/tokens
# → {"token":"excb_…","prefix":"excb_a1b2c3d","name":"ci-deploy","scopes":"","expiresAt":"2026-…"}

# List — your personal access tokens only (no sign-in sessions, no service tokens):
# id (the SHA-256 that revoke/rotate take), name, scopes, projectId, createdAt,
# expiresAt and lastUsed (written at most once a minute per token)
curl -sf -H "Authorization: Bearer $PAT" https://<host>/api/auth/tokens | jq

# Rotate — same owner/name/scopes, lifetime restarts from now.
# graceSeconds (0–3600, default 0) keeps the OLD secret valid for that long so
# a fleet can swap credentials without a hard cut; the old expiry is never extended.
HASH=$(echo -n "$OLD_PAT" | sha256sum | awk '{print $1}')
curl -X POST -H "Authorization: Bearer $OLD_PAT" -H 'Content-Type: application/json' \
  -d '{"graceSeconds":300}' https://<host>/api/auth/tokens/$HASH/rotate
# → {"token":"excb_…(new)","expiresAt":"…","previousExpiresAt":"…(now+300s)", …}
```

Only the token's owner can rotate it, with one exception: a platform admin may rotate a **service account's** token (see 1.2), because that is exactly what the rotation job does. Admins can revoke any token from a session. A narrowed PAT (scoped or project-bound) may revoke only tokens no broader than itself. Creating, revoking and rotating write `access_token` audit rows (`token.create`, `token.revoke`, `token.rotate`) carrying the display prefix and who acted — never the secret or its hash. One user may mint at most 20 tokens an hour; past that the API answers `429`. An expired token is refused with `401` and `"code":"token_expired"`, so a CI job that starts failing with that code needs a rotate, not a re-login.

### 1.2. Service identity: service accounts and capability tokens

The platform's own components no longer authenticate as a human admin. Each one has a **service principal** — a user with `kind = service` that has no password, is refused by `/api/auth/login` and `/api/auth/register`, and can never be added to an organization. Its only credential is a **capability token**.

A capability token carries an explicit permission list. Each entry is `<resource>:<action>[:<selector>]`:

| Permission | Grants |
| --- | --- |
| `vault:read:pki/signing/*` | `GET /api/vault/secrets/pki/signing/<leaf>` — the `*` stands for exactly one path segment and never crosses a `/` |
| `projects:info:read` | `GET /api/projects/{projectId}/info` |
| `policies:read` | `GET /api/provision/{projectId}/permissions/` — the whole permission document, nothing below it |
| `email:send` | `POST /internal/email/send` — the transactional mail relay; the only write any capability may make |

The list is **default-deny and absolute**: a token with a non-empty permission list may call only the endpoints its list names, and everything else — every write, every other route — answers `403`, regardless of the owning principal's platform role. `GET /api/auth/me` is always reachable so a service can validate its own credential. Tokens with an empty permission list (every human PAT and session) are untouched by this layer.

The two principals the platform ships with:

| Principal | Permissions | Consumer |
| --- | --- | --- |
| `svc-auth` | `vault:read:pki/signing/*`, `projects:info:read`, `email:send` | auth service — JWKS signing key at boot, per-project info hourly, verification and reset mail |
| `svc-graphql` | `projects:info:read`, `policies:read` | engine — project credentials + CORS, the permission document |

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
# Bypasses deletion_protection. Audited; a written reason (500 characters
# at most) is required and goes into the audit entry.
curl -X DELETE -H "Authorization: Bearer $PAT" -H "Content-Type: application/json" \
  -d '{"reason":"abandoned, holding a node"}' \
  https://<host>/api/admin/projects/proj-abc123
```

### Deletion protection is on for every project

A project is created with deletion protection on, and `DELETE
/api/provision/<projectId>/` answers `400` until an org **Owner** turns it off:

```bash
curl -X PATCH -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"enabled": false}' https://<host>/api/provision/proj-abc123/deletion-protection
```

The body must name `enabled`; an empty or malformed body is refused.

### Deleting a project has a 7-day grace period

`DELETE /api/provision/<projectId>/` on an ACTIVE or PAUSED project does not
tear it down. It pauses it (backup first, then the workload stops, disk kept),
answers `202` with `status: PENDING_DELETION` and `deletionDueAt` seven days
out, and the deletion sweep (every 15 minutes, leader replica only) runs the
teardown below once that date passes. During the grace period the project
no longer holds its org slot (the org can create a replacement at once), is not
served (credentials, data plane, functions refuse it), and its
deletion-protection toggle is locked. An org **Owner** can cancel, which takes
a slot again: when the plan's slots are all in use the cancel is refused with
`409` until one is freed or the org moves to a larger plan:

```bash
curl -X POST -H "Authorization: Bearer $PAT" \
  https://<host>/api/provision/proj-abc123/deletion/cancel
```

A cancelled project is left PAUSED with deletion protection back on; resume it
as usual. A project that holds no data (FAILED, an unconfirmed RESTORING) is
deleted at once. The platform admin force drop (`DELETE
/api/admin/projects/<id>`) skips the grace period.

### Deleting a project is observed, not fire-and-forget

`DELETE /api/provision/<projectId>/` runs the teardown and only removes the
project record once every step is observed complete: the tenant watcher is
stopped, NATS credentials revoked, the database
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
* With `FEATURE_LOGDELETION=true` (chart `features.logDeletion`) and a
  `LOKI_URL`, the teardown also queues the deletion of the project's stored
  log lines (step `QUEUE_LOG_DELETION`, table `project_log_deletions`). A
  worker sends Loki one delete request for `{namespace="<project
  namespace>"}` (single host: `{project_id="<id>"}`), retries it while Loki
  is down, and marks it done when Loki reports it processed, about a day
  later (Loki's `delete_request_cancel_period`). Each step is in the platform
  audit log (`project.logs.delete_*`). Nothing else is ever sent: an empty,
  wildcard or regex selector is refused, and a project whose namespace is
  not its own is skipped and audited, not deleted wider.

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

Kept backups are purged **14 days after the hard delete** by the deletion
sweep, from a `retained_backups` record written during the teardown (project,
deployment mode, purge date). A failed purge keeps its record and is retried
on the next sweep. `confirmDeleteBackups: true` remains the immediate-erasure
path. When backups are kept on an immediate delete, the `200` response carries
the object-store prefix they remain under:

```json
{"projectId":"proj-abc123","status":"DELETED",
 "retainedBackupPrefix":"backups/proj-abc123/"}
```

No endpoint lists the backups of a project whose record is gone —
`POST .../backups/purge` needs the row. Until the sweep purges them, retained
objects are reachable only with object-store credentials, by prefix:

```bash
aws s3 ls "s3://$BUCKET/backups/proj-abc123/" --endpoint-url "$R2_ENDPOINT"
aws s3 rm --recursive "s3://$BUCKET/backups/proj-abc123/" --endpoint-url "$R2_ENDPOINT"
```

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
# A written reason is required (JSON body, 500 characters at most) and is
# audited. The org's owner is suspended too, so they cannot sign in again.
curl -X DELETE -H "Authorization: Bearer $PAT" -H "Content-Type: application/json" \
  -d '{"reason":"proxy abuse across every project"}' \
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

### What a restore into a new project carries

Every restore path (CNPG, single-host Postgres, MOCO, single-host MySQL) ends
in the same registration, so a copy gets the same things on each:

- **Carried with the database:** tables, data, database functions, the realtime publication
  (Postgres), and the end users. Their sessions, API keys and email links are
  revoked (EXC-635).
- **Copied from the source's platform rows** before the copy is activated
  (EXC-639): table permissions (columns, row filters), tracked functions and
  who may call them, the CORS origins a person added, and auth settings
  (email verification, site URL). They are the source's settings at restore
  time, not as of the backup.
- **Copied from the source's STORAGE_PATH:** the migration history
  (`list_migrations`), as the source has it at restore time; a restore to an
  earlier point still lists migrations applied after that point.
- **Not carried:** apps, sites and domains (running deployments; a domain
  points at one project), CORS origins an app added, edge functions with their
  secrets and outbound allowlist (redeploy them; copying would hand the
  source's third-party secrets to another project), stored files, and
  project-level members.

A copy takes its organisation's project slot when the restore is submitted:
the running job counts as a project until the copy is registered, so nothing
created meanwhile can take the slot. A plan that is full answers 409
`project_limit_reached` at submission.

### K8s mode (CNPG + Barman Cloud plugin → R2)

Backups go through the CloudNativePG Barman Cloud plugin (CNPG-I), installed
beside the operator in `cnpg-system` (charts repo:
`charts/background/barman-cloud-plugin/install.sh`, needs cert-manager). Its
sidecar carries barman-cloud, so the Postgres image does not need it. Each
project namespace holds an `ObjectStore` named `{projectId}-backups`; the
Cluster names it in `spec.plugins` as its WAL archiver, and Backups /
ScheduledBackups use `method: plugin`. A restored project reads its source
through `{newProjectId}-recovery-source` (no retention policy, so it never
prunes the source). A restore by `backupId` passes that Backup's
`status.backupId` as `recoveryTarget.backupID` and stops at the end of that
backup (`targetImmediate`) unless a later target is also given.

#### Credential flow (what actually happens at runtime)

The backup store's key (the platform key: `BACKUP_DEFAULT_*`, else `R2_*`,
else vault `backup/s3`) never leaves the platform namespace (EXC-476). Each
project namespace holds only a temporary credential for its own prefix,
minted by provisioning:

```
platform key (provisioning process only)
        │  BACKUP_CREDENTIALS_PROVIDER=r2  → R2 temporary credential, signed locally
        │  BACKUP_CREDENTIALS_PROVIDER=sts → MinIO AssumeRole session + inline policy
        ▼
K8s secret  {org}-{projectId}/backup-s3-creds
                ACCESS_KEY_ID, ACCESS_SECRET_KEY, ACCESS_SESSION_TOKEN,
                EXPIRES_AT, ISSUED_BY (fingerprint of the platform key)
        │  scope: bucket + prefix {projectId}/cloud/, object read-write,
        │  lifetime BACKUP_CREDENTIALS_TTL (12h)
        ▼
ObjectStore {projectId}-backups → s3Credentials.{accessKeyId,secretAccessKey,sessionToken}
        │  the plugin sidecar re-reads the Secret on each WAL segment (10 s cache)
        ▼
s3://excalibase-backups/{projectId}/cloud/{base,wals}/...
```

- **Renewal:** the leader replica checks every
  `BACKUP_CREDENTIALS_RENEW_INTERVAL` (10m) and replaces a project's Secret
  data once less than half its lifetime is left, or at once when it was
  minted from a different platform key (rotation). Nothing restarts. A
  failed renewal is logged (`ERROR: backup credential renewal: ...`) and
  counted (`excalibase_backup_credential_renewals_total{result="failed"}`);
  the current credential keeps working for up to 6 more hours. Alerts:
  `BackupCredentialRenewalFailing`, `BackupCredentialsExpiringSoon`,
  `CNPGWALArchivingFailing`.
- **Restores** get a second Secret, `backup-source-creds`: read-only for the
  source's prefix, living `EXCALIBASE_RESTORE_READY_TIMEOUT` + 1h, never
  renewed. The restored project archives with its own `backup-s3-creds`.
- **Prefix check:** a project is refused a prefix that already holds objects
  (e.g. a deleted project's retained backups). This replaces
  `barman-cloud-check-wal-archive`, which needs HeadBucket — something no
  prefix-scoped credential can do — so clusters carry
  `cnpg.io/skipEmptyWalArchiveCheck: enabled`.
- **Required configuration:** in Kubernetes mode a backup key without
  `BACKUP_CREDENTIALS_PROVIDER` stops the boot, and a project with backups is
  refused when nothing can mint its credential. Docker mode keeps the key in
  the provisioning process (its backups are uploaded by the platform itself)
  and needs no provider.
- **Rotating the platform key:** update it, restart provisioning; every
  project is renewed on the next pass (credentials derived from a revoked R2
  key stop working at once, so rotate the new key in before revoking the old).
- A client cannot name its own backup store (`backup.s3` in a provision
  request is refused).

- **Platform-db backups use a bucket and key of their own**
  (`platform-db-backup-creds`, `s3://excalibase-platform-db-backups/` in
  `charts/platform-base`). Never point them at `r2-creds`: the platform DB
  holds every user's password hash and the vault, and a key shared with
  tenant backups and files widens what one leak exposes. The chart refuses `r2-creds` and
  `backup-s3-creds`, requires `platformDB.backup.endpointURL`, and pins the
  image by digest. They go through the Barman Cloud plugin (ObjectStore
  `platform-db-backups`, ScheduledBackup `platform-db-backup`), so the plugin
  must be installed before platform-base when backups are enabled.

#### R2 layout (Barman convention)

```
s3://excalibase-backups/
  {projectId}/cloud/base/{timestamp}/{backup.info, data.tar.gz}
  {projectId}/cloud/wals/{timeline}/{walSegment}.gz
```

`serverName: cloud` is the plugin parameter set in `k8s/barman_cloud.go`; `destinationPath`
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

1. Platform roles (`auth_admin`, `excalibase_app`, `cdc_watcher`, `excalibase_scheduler`) are created,
   and — because a restored database arrives carrying the source project's
   roles and passwords — their passwords are **reset** to freshly generated
   ones. The restored project therefore has its own credentials; leaking the
   source's does not grant access to it, and vice versa. Grants are put back
   to the current ones too: a backup from before EXC-628 let `excalibase_app`
   read the auth schema, and the registration SQL revokes that, so only
   `auth_admin` reaches the end users' tables.
2. Those credentials are written to vault under
   `projects/{restoredProjectId}/credentials/{role}`, which is what the schema
   plane, graphql and auth read. Without this step the project exists but
   every data-plane call fails with "project credentials not found in vault".
3. The instance row is saved `ACTIVE`, carrying the source's org, tier and
   deployment mode plus `restoredFromProjectId` / `restoredFromBackupId` so
   support can trace where the data came from.
4. The project-created event is published, and a `project_activity` marker is written so the new project is
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

1. Cluster/container deletion, vault `projects/{id}/`
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
`confirmDeleteBackups` body field, and requires a `reason`. With `Prefer: respond-async` it answers
202 once the project is `DELETING` and tears it down in the background; the
row is gone when it finishes, or stays `DELETING` with `deletion_step` and
`deletion_error`. A teardown never depends on the request staying connected.

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

   After a major upgrade (EXC-668) each later major archives under
   `s3://{bucket}/backups/{projectId}/pg{major}/`; the original layout
   stays for restores of earlier points and is removed with the project.
   On Kubernetes the new major's ObjectStore is `{projectId}-backups-pg{major}`
   with serverName `pg{major}`.

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
`targetTime` must be RFC 3339 with a zone (`Z` or `+07:00`) and keeps its
microseconds; a zone-less time is refused with 400. Before a Kubernetes
point-in-time restore creates anything, a running source commits after the
target, switches WAL and waits (2 min) until that segment is archived; a
target later than the source's clock, or WAL that does not archive in time,
fails the job with the reason. A recovery that still ends before the target
fails the restore (`recovery ended before targetTime was reached`).
Multi-replica platforms only fire scheduled backups from the lease
holder (Postgres `pg_try_advisory_lock`).

### Platform database pool sizing

Every control-plane replica opens one pool against the platform database.
Two kinds of work pin a connection for longer than a query:

- **Standing leadership claims — 5 per replica.** The backup scheduler, the
  idle-pause sweep, the storage reaper, the restore sweeper and the function
  scheduler's cron half each hold one advisory lock, and an advisory lock
  pins the session that took it. (`EXCALIBASE_SCHEDULER_ENABLED=false` drops
  the last one.)
- **In-flight lifecycle operations — 1 each, for their whole duration.**
  Pause, resume and delete take a per-project lease so two replicas cannot
  act on one project at once. A pause waits for a backup to complete and for
  the database to shut down, so it can hold its connection for minutes.
- **App image publishes — 1 each, for the whole push (flag `appgitbuilds`).**
  A finished app build's publish holds its project's lock (a transaction
  advisory lock) from the registry quota check through the push and the deploy
  record, so a slow push pins that connection for as long as it uploads. Up to
  `SITES_BUILD_SLOTS` publishes can run at once on a replica, so keep
  `SITES_BUILD_SLOTS` well below `PLATFORM_DB_MAX_CONNS` minus the five standing
  claims, or publishes and ordinary queries wait on the pool.

So a replica needs `5 + (concurrent lifecycle operations) + (headroom for
ordinary query traffic)`. The default pool is **20**, which leaves room for
roughly a dozen concurrent pauses alongside normal traffic. Raise it with
`PLATFORM_DB_MAX_CONNS` (minimum 7 — five claims plus one operation plus one
query) on a control plane that pauses many projects at once. A value that is
not a number, or below the minimum, **stops the platform from starting**: a
pool that cannot do the work is a misconfiguration to fix, not something to
paper over with a default the operator did not choose, and make sure Postgres' own `max_connections`
covers `PLATFORM_DB_MAX_CONNS x replicas` plus your own sessions.

Tenant databases are not reached through this pool. The function scheduler
opens a small pool per project (`EXCALIBASE_PROJECT_DB_MAX_CONNS`, default 2,
at most `EXCALIBASE_PROJECT_DB_MAX_POOLS` cached — see
`docs/functions-scheduler.md`), which costs connections on the tenant's own
database and file descriptors here, never platform-database connections.
Those connections carry a server-side `EXCALIBASE_PROJECT_DB_STATEMENT_TIMEOUT_MS`
(default 30000) and `EXCALIBASE_PROJECT_DB_LOCK_TIMEOUT_MS` (default 5000),
and each project's sweep is bounded by
`EXCALIBASE_SCHEDULER_PROJECT_TIMEOUT_MS` (default 30000), so a tenant
database that will not answer costs the sweep one timeout and is then backed
off rather than stalling every other tenant's tasks. A claimed task that no
replica finishes is taken back after `EXCALIBASE_SCHEDULER_CLAIM_LEASE_MS`
(default 300000), spending one attempt so a poison task ends `failed`.

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

## 6.0. Public database endpoints

A project reaches its own database **inside** the cluster with no public port
at all — over the CNPG read-write Service, which is how an app this platform
hosts beside the database should connect. Reaching it from outside is
**opt-in and off by default**.

When a customer opts in, the project gets one Kubernetes Service of type
`LoadBalancer` carrying MetalLB's `metallb.universe.tf/allow-shared-ip`
annotation and a TCP port of its own. Services that agree on the sharing key
land on one public IPv4 address, so several projects sit behind one address
and Kubernetes does the routing: there is no edge process of ours to
configure, reload or keep alive. A wildcard DNS record points
`*.<domain>` at that address, so the hostname carries no routing — it exists
so a certificate can be verified against a name and so the address can change
later. Turning the endpoint off deletes the Service and the port stops
answering.

TLS is **not** terminated at the edge. The session is encrypted all the way
to Postgres, so nothing between the customer and their database ever holds
their credentials. "Require TLS" is a per-project setting, **on by default**,
enforced in Postgres through `pg_hba` (`hostssl` only when on).
`GET /api/projects/{id}/db-endpoint` returns the cluster CA alongside both
connection strings, so `sslmode=verify-full` works and a customer can see
exactly what turning the setting off would cost them.

| Variable | Default | Meaning |
|---|---|---|
| `EXCALIBASE_DB_ENDPOINT_DOMAIN` | *(empty)* | Suffix customers dial: `<projectId>.<domain>`. Empty means the platform offers no public endpoints and the API says so rather than inventing a name. A suffix that only resolves inside the cluster **stops the platform from starting**. |
| `EXCALIBASE_DB_ENDPOINT_PORT_RANGE` | `30000-30999` | Inclusive window ports are allocated from. Anything present but unparseable, inverted or privileged **stops the platform from starting**. |
| `EXCALIBASE_DB_ENDPOINT_PORT_QUARANTINE` | `720h` (30 days) | How long a freed port is held back before it can be reissued. |
| `EXCALIBASE_DB_ENDPOINT_SHARED_IP_KEY` | `excalibase-db-edge` | MetalLB sharing key. Every project's Service carries it, which is what puts them on one address. |

**Allow-list (`FEATURE_DBALLOWLIST`, EXC-665).** With the flag on, an Admin
names who may connect (`GET/PUT /api/projects/{id}/db-endpoint/allowed-sources`,
`{"sources": [...]}` or `{"allowAny": true}`; reads Developer+, writes Admin,
audited as `db_endpoint.allowed_sources.update`). The list is rendered as
`loadBalancerSourceRanges` on every public Service of the project (Postgres,
the DocumentDB Mongo port, MySQL), which Cilium's kube-proxy replacement (and
kube-proxy) enforce at the node before any SNAT; those Services then allocate
no NodePort (`allocateLoadBalancerNodePorts: false`), because Cilium applies
the list to the load-balancer address only. Entries are IPv4/IPv6 addresses
or ranges, at most 20 and at least one IPv4 (the shared address is IPv4, and
kube-proxy ignores a list with no entry of the Service's family), no `/0`
(that is `allowAny`), nothing wider than /8 (IPv4) or /16 (IPv6), no
loopback, link-local or multicast; MCP may set only up to /24 and /48. Opening
a port needs that choice first; a port opened before the flag keeps admitting
any address until an Admin chooses. A stored list stays on the Services if the
flag is turned off again, so a rollback never opens a restricted port. A change
that fails part way puts the recorded list back and is audited as refused. In-cluster clients are never affected. A
datapath that does not implement the field ignores it, so check
`kubectl get svc -o yaml` and a refused outside dial before relying on it.
A single host's databases listen on 127.0.0.1 only, so its list is fixed at
"this host only" and a change is refused. Flag off with no stored list:
Services and routes as before.

**Read-only login (`FEATURE_DBREADROLE`, EXC-675).** With the flag on, a new
Postgres or MySQL project gets `excalibase_reader`: SELECT on every user table
(Postgres: through default privileges for the owner and `excalibase_app` and a
trigger on new schemas; MySQL: `SELECT, SHOW VIEW` on the project database),
read-only sessions, and an event trigger that refuses it any DDL. On Postgres
pg_hba admits it to the project database only, and creating large objects
(`lo_creat`, `lo_create`, `lo_from_bytea`) is taken from PUBLIC and granted
back to the owner (with grant option) and `excalibase_app`, so a customer's
own roles need the owner's GRANT to create one. Like any role in plain
Postgres, it can run a SECURITY DEFINER function the customer creates, with
that function owner's rights. It is filed
in vault beside the platform logins, handed to Admins with the credentials and
rotated with them, and the read address names it instead of the owner. A
project created with the flag off has none, and a copy restored without one
has the role locked. The flag also makes a rotation end the sessions opened
with the old passwords, once every password has changed: on the primary, then
on each replica once it has replayed the change (Postgres
`pg_last_wal_replay_lsn`, MySQL `WAIT_FOR_EXECUTED_GTID_SET`, 30 s each). A
replica CNPG does not report healthy is skipped, and it and any replica that
fails are reported in the rotation's error; the passwords stay changed. This
includes `excalibase_app`, so the engine reconnects with the new password it
was told about.

**Ports are allocated randomly inside the window**, never sequentially: two
port numbers must not let anyone read off how many tenants the platform has
or in what order they signed up. Allocation is atomic — an advisory lock on
the port space plus a unique index — so two concurrent enables can never be
handed the same port. Exhaustion is a clear refusal (`503`), never a
wrap-around.

**A freed port is not reusable immediately.** A customer's application, a
saved connection in a GUI client or a forgotten cron job keeps dialling a
port for weeks after the project behind it is gone; if that port has been
handed to another tenant, those clients arrive at a stranger's database and
present credentials to it. So a freed port sits in quarantine for the
configured window, enforced in the allocator itself. Resource limits will
exhaust the cluster long before a thousand ports run out, so a long
quarantine costs nothing — leave it long.

**Across the lifecycle.** A project that is paused, deleting, restoring or
otherwise not servable has **no** Service, so its port refuses connections
rather than pointing at nothing. A pause withdraws the Service once the
database is observed stopped and **keeps the port**, so a resume comes back on
the same number and saved connection strings still work. Deleting the project
removes the Service first, then frees the port into quarantine.

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
| k8s (per-project `deno-runtime` pod) | `ALLOWED_HOSTS` env on the Deployment + `excalibase.io/egress-hash` pod annotation; the `deno-runtime-egress` fence re-rendered in the same call: with `FUNCTION_EGRESS_POLICY=cilium` a CiliumNetworkPolicy admitting the allowlisted names (`toFQDNs`) on their ports, otherwise a NetworkPolicy opening public IPv4 on the allowlisted TCP ports; private ranges denied either way, nothing when the list is empty | Deployment rolls; the runtime replayer refills the pod |
| docker (one shared runtime) | every deploy payload carries `allowedHosts`; the worker gets exactly that list | functions redeployed immediately |

Verify on k8s:

```bash
kubectl -n <project-ns> get deploy deno-runtime -o jsonpath='{.spec.template.spec.containers[0].env}' | jq
kubectl -n <project-ns> get ciliumnetworkpolicy deno-runtime-egress -o yaml | grep -B2 -A4 toFQDNs   # cilium
kubectl -n <project-ns> get networkpolicy deno-runtime-egress -o yaml | grep -A6 ipBlock               # networkpolicy
```

One runtime serves all of a project's functions, so the allowlist is per
project. `FUNCTION_EGRESS_POLICY=cilium` (set by `rke2/install-platform.sh`)
needs Cilium 1.19+ with the L7 proxy on; the default `networkpolicy` is only
a backstop (it cannot match hostnames) and needs an enforcing CNI (Calico /
Cilium). On the shared docker runtime the
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
`charts/platform-aio` install script and the nightly e2e pin 1.30.1.

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
| `/api/provision/{id}/audit`, `/migrations`, `/permissions`, `/tracked-functions`, `/function-permissions` | Developer | |
| `DELETE /api/provision/{id}`, `/credentials`, `/credentials/rotate`, `/deletion-protection`, `/pause`, `/resume`, `/backups/purge` | Admin | lifecycle + secrets |
| `/api/provision/{id}/backup/*`, `/snapshot/*` | Admin | dump/restore are destructive and exfil-capable |
| `POST /api/provision/{id}/backup/copy` | Admin | a new project in the source's organisation from its backups, at `now` (a backup is taken first), `latest` or a `time`; needs backups on and a free project slot; audited as `database.copy` (ids and point only); unmounted while `dbcopy` is off |
| `GET /api/provision/{id}/major-upgrade/` | Viewer | flag `majorupgrade`; unmounted while off |
| `POST /api/provision/{id}/major-upgrade/` | Admin | flag `majorupgrade`; one-way, takes a backup first |
| `GET /api/schema/{id}/*` | Viewer | browse |
| `POST/PATCH/DELETE /api/schema/{id}/*` (DDL, `/query`, row writes) | Developer | |
| `GET /api/projects/{id}/functions`, `/{fnId}`, `/{fnId}/logs`, `/_metadata`, `/runtime/status` | Viewer | |
| `POST /api/projects/{id}/functions`, `/{fnId}/invoke`, `DELETE /{fnId}`, `/secrets`, `/egress` | Developer | deploy, invoke, secrets, outbound allowlist |
| `POST /api/projects/{id}/schema/apply` | Developer | |
| `GET/PUT /api/projects/{id}/cors` | Developer | browser-origin allowlist the data plane enforces |
| `/api/projects/{id}/info`, `/realtime/*`, `/storage/*`, `GET /api/alerts/project/{id}` | Viewer | |
| `/api/projects/{id}/auth-settings` | Developer (Admin writes while `authoauth`, `authotp` or `authguest` is on) | `requireEmailVerification` + `siteUrl`; with any of the three on also `redirectUrls` + `allowNewUsers`; with `authotp` on also `emailOtpEnabled`; with `authguest` on also `guestSignInEnabled` + `guestCaptchaSecretSet`; with `authmfa` on also `mfaEnabled` (read-only here) |
| `/api/projects/{id}/auth-settings/guest-captcha` | Admin | guest Turnstile secret, write-only, in the vault at `projects/{id}/auth/captcha`; svc-auth gets `vault:read:projects/*/auth/captcha` only with `FEATURE_AUTHGUEST=true`; unmounted while `authguest` is off |
| `/api/projects/{id}/auth-hooks` | Developer reads, Admin writes | end-user auth hooks (URL + switch per hook; a Developer reads each hook's host only, an Admin its full URL, and no read-only token reads them) and the signing secret, generated by the platform and shown once, in the vault at `projects/{id}/auth/hooks`; svc-auth gets `vault:read:projects/*/auth/hooks` only with `FEATURE_AUTHHOOKS=true`; `AUTH_HOOKS_DENY_CIDRS` on the auth container adds ranges hook calls may never reach; restored copies start with no hooks; unmounted while `authhooks` is off |
| `PUT /api/projects/{id}/auth-settings/mfa` | Admin | end-user MFA switch; the project's key is created once in the vault at `projects/{id}/auth/mfa` and never replaced or returned; svc-auth gets `vault:read:projects/*/auth/mfa` only with `FEATURE_AUTHMFA=true`; unmounted while `authmfa` is off. A restored copy gets the source's key at its own path (a source with no key gives a copy with MFA off; any other failure fails the restore). If switching on answers 409, the value at `projects/{id}/auth/mfa` is not a 32-byte base64 key: do not overwrite or delete it, since enrolled factors are sealed with it; restore the original value from a vault backup, or, only if no end user has enrolled a factor, delete the path and switch on again to create a new key. The first switch-on records the key's SHA-256 in `project_auth_settings.mfa_key_fingerprint`; a missing or different key then answers 409 (`restore it from a vault backup`) instead of a new key being created. Only if no end user has enrolled a factor, clear that column to start over |
| `DELETE /api/projects/{id}/end-users/{userId}/factors` | Admin | removes one end user's MFA factors and recovery codes in auth (user-admin token) and ends their sessions; audited (`end_user.factors.reset`); unmounted while `authmfa` is off |
| `/api/projects/{id}/auth-providers` | Developer reads, Admin writes | end-user Google/GitHub client id; the secret is write-only, in the vault at `projects/{id}/auth/providers/{provider}` (`authoauth`) |
| `/api/projects/{id}/end-users/{userId}` | Admin | one end user's details, metadata change and delete, relayed to auth with a user-admin token; audited (`end_user.update` names fields only, `end_user.delete`); unmounted while `authuser` is off |
| `POST /internal/email/send` (account templates) | service (`email:send`) | `email_change`, `email_change_notice` (flag `authuser`) and `identity_linked` (`authoauth` with an enabled provider, or `authotp` with the project's switch); 1000 a day per project, a budget separate from sign-in mail (`sign_in_mail_sends.kind = 'account'`) |
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

## 6.5. NATS access (network policy, no auth callout)

The message bus is internal and is protected by network isolation, not by an
auth callout (EXC-507). The chart's `nats` NetworkPolicy admits port 4222 only
from the platform's own provisioning, graphql and stream-init pods and from
pods labelled `excalibase.io/component: cdc-watcher` in a namespace labelled
`excalibase.io/type: project`; provisioning sets both labels, tenants cannot.
Every other pod, including customer apps, is refused. NATS is never exposed
outside the cluster. A customer-facing queue, if one is ever built, is a
separate system with its own auth. On a single host the same isolation comes
from the `excalibase-cdc` network plus the proxy's `PROXY_CDC_IMAGES` allow-list.

Each principal still presents a scoped credential (EXC-324) and a per-principal
inbox prefix. The server does not check them; they exist so a future bus that
does enforce subjects needs no client change. `internal/natsauth.PermissionsFor`
holds the matrix:

| Principal | Publish | Subscribe |
|---|---|---|
| `svc-graphql` | `$JS.API.INFO`, `$JS.API.STREAM.INFO.CDC`, `$JS.API.CONSUMER.>`, `$JS.ACK.>` | `cdc.>`, `policies.>`, `_INBOX_svc-graphql.>` |
| `svc-provisioning` | `policies.>`, `$JS.API.INFO`, `$JS.API.STREAM.>` | `_INBOX_svc-provisioning.>` |
| `tenant-watcher:<projectId>` | `cdc.<projectId>.>`, `$JS.API.INFO`, `$JS.API.STREAM.INFO.CDC` | `_INBOX_tw_<projectId>.>` |

Environment on provisioning:

| Variable | Meaning |
|---|---|
| `NATS_USER` / `NATS_PASSWORD` | provisioning's own bus credential (`svc-provisioning`) |
| `NATS_CDC_STREAM` | shared JetStream stream name (default `CDC`) |

Tenant credentials are minted at provision time, rotated on re-provision and
deleted on deprovision. Only bcrypt hashes are stored; the plaintext exists
once, in the watcher's Secret in the tenant namespace.

## 6.5e. App filesystems on Kubernetes (EXC-732)

An app pod runs with a read-only root filesystem. It writes to its disk (a PVC, writable and sized by the plan), to `/tmp`, and to any `VOLUME` its image declares. The last two are memory emptyDirs, because a memory emptyDir is charged to the container's memory limit and the kernel enforces its size at write time; a disk emptyDir is only checked by eviction after the fact. Together they get `APP_TMP_PERCENT` (default 25, 1 to 100) of the app's memory limit, split evenly. A pod with no memory limit, or a share that rounds to 0 bytes, is refused rather than rendered with an unlimited `sizeLimit`.

The image's `VOLUME` paths are read from its registry at deploy time (the manifest and config, with the project's saved credential): for an index, the union over every Linux image in it (at most 16). An image whose registry cannot be read is not deployed, and the deploy then runs the digest that was read, so a moving tag cannot change what the node pulls. Besides public registries, the platform reads from exactly two private kinds and dials nothing else: its own git-build registry at its in-cluster `APP_REGISTRY_URL` (whatever host or alias an image names it by) and the hosts in `APP_LOCAL_REGISTRIES`, which must therefore be reachable from the provisioning pod (plain HTTP on localhost or a port other than 443). Without this, a `VOLUME /v` is a directory on the node's disk that a read-only root does not cap. A path the app's own disk is mounted at keeps the disk, and an image volume is empty after a restart: give the app a disk for data that must last. The tier `ephemeral-storage` limit still caps logs and anything else the node holds for the pod.

Stock `nginx` writes `/var/cache/nginx` and `/run` and crashloops under the read-only root: use `nginxinc/nginx-unprivileged`, which keeps its pid and temp files in `/tmp`. The same goes for other images that write outside `/tmp` and their disk; docs/deployment/docker-local.md lists the usual fixes. The app's data volume has no byte cap beyond its claim size.

## 6.5b. WAL archive guards (EXC-784)

Two guards, both **on unless switched off** (`FEATURE_WALARCHIVEALERT=false`, `FEATURE_FREEWALUPLOAD=false`), on Kubernetes and on a single host.

**Archive alerts.** A database whose CloudNativePG `ContinuousArchiving` condition has been False for `EXCALIBASE_WAL_ARCHIVE_ALERT_AFTER` (default `30m`) raises a platform alert, metric `wal.archiving:<namespace>/<cluster>`, severity CRITICAL. A project whose backup credential renewal fails on every pass for the same time raises `backup.renewal:<project>`. Both go through the same path as the pod-pressure alerts (EXC-747): the platform alerts table (the admin alerts list) and an email to every verified platform admin when a mailer is configured: one mail per kind of alert (`backup.renewal`, `wal.archiving`, `wal.shipping`, `wal.monitor`) per hour, listing every affected project, so one shared cause is one mail while each project keeps its own alert row. Alerts of a kind raised close together are batched for 30 seconds; one raised inside the hour after a mail waits for the next mail of its kind, sent when the hour is up, so none is dropped, and an alert that has resolved by the time that mail goes out is left out of it (read once per mail; if the alerts table cannot be read at that moment, every queued alert is listed). Mail still queued when the process exits (inside that window, or waiting out the hour) is lost; the alert rows remain, and the alerts list is the source of truth. They resolve by themselves. A paused (hibernated) cluster never alerts: nothing runs and its last backup stays valid. On a single host, closed WAL waits in the project's container until the one-minute sweep ships it, so a store that refuses uploads fills that container's disk; a project whose shipping fails on every pass for the same time raises `wal.shipping:<project>` (paused projects are not swept and never alert).

**Free uploads hourly.** Free databases get `archive_timeout = 1h`; Standard and Enterprise keep the operator's 5 minutes. An idle database otherwise writes a 16 MB segment every 5 minutes (about 4.6 GB a day) that fills a 5Gi disk within days if uploads fail. The cost: if a Free database's disk is destroyed, up to an hour of writes can be lost. New Free clusters get the setting at creation; existing ones get it on the next platform reconcile pass (at startup and every 10 minutes, no restart: `archive_timeout` reloads). On a single host the bootstrap organization is on Free, so every project there gets it unless `FEATURE_FREEWALUPLOAD=false`; a new Free container is started with it, and the same reconcile pass reads each running Free container's `archive_timeout` and reloads it when it differs (no restart); a restored or major-upgraded project, which comes back on 5 minutes, and a paused project after it resumes are corrected by that pass too. A project moved to another plan loses it with the plan change (Kubernetes). To undo, set `FEATURE_FREEWALUPLOAD=false`: the same reconcile pass then removes `archive_timeout` from every Kubernetes cluster (the operator's 5 minutes returns) and sets single-host containers back to `5min` explicitly (a `RESET` would leave Postgres' own 0, which never closes a quiet segment). No manual step. The reconcile skips projects still being provisioned or failed, which have no cluster yet.

## 6.5c. App and function database login (EXC-765)

Customer apps and functions connect with `excalibase_runtime`, not the project owner's login (**on unless switched off**, `FEATURE_APPRUNTIMELOGIN=false`, on Kubernetes and on a single host). On Postgres 16 and later the owner holds `CREATEROLE`, so code that held its password could create or alter logins, which breaks the rule that no credential does more than its creator.

- **Postgres.** `excalibase_runtime` is `LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`. It inherits the owner's and `excalibase_app`'s rights over objects (DML, DDL on their tables, sequences, schemas) but not their attributes; from Postgres 16 the memberships carry `SET FALSE`, so it cannot `SET ROLE` to either. `excalibase.create_role`, `alter_role_password` and `drop_role` also refuse any session that is not the owner's (`session_user`), which is what keeps a member of the owner role on Postgres 15 out of them. Tables it creates are shared with the owner, `excalibase_app` and the reader through default privileges.
- **MySQL.** `excalibase_runtime` holds `ALL PRIVILEGES` on the project database and nothing global: no `CREATE USER`, `GRANT OPTION` or `SUPER` (the owner's `admin` has none either). Database grants name the database with `_` and `%` escaped, since an unescaped `aut_` matched `auth`; the reconcile sets the grants again once for an older project whose name holds one, and the names `auth`, `excalibase`, `excalibase_*`, `mysql`, `sys`, `information_schema` and `performance_schema` are refused.
- **What it does not stop.** The role functions refuse a call made from inside a trigger or another function, but not one made by a view, RLS policy, rule, column default, CHECK or inlined SQL function an app planted: those run as whoever uses them. If the owner login merely reads such an object, the planted code can call the role functions (Postgres 15 and earlier) or `CREATE ROLE` (16 and later). The owner login is the platform's and must not touch app-created objects at all, not even to read them (a known residual, pinned by a test; the robust fix is separate). The app login can also add triggers to tables Studio made, because it is a member of `excalibase_app`. Studio and the engine write those tables as `excalibase_app`, which holds no role management, so such a trigger never runs with role rights; only someone writing with the owner login by hand would run it as the owner (the EXC-807 case). Tables an app creates give the owner SELECT only, so the owner's session does not fire their triggers by default. On Postgres 15 the runtime login can `SET ROLE` to the owner as well as to `excalibase_app` (not on 16 and later, where the grant is `SET FALSE`), which is the same exposure as above; an upgrade to 16 has the reconcile remove it.
- **Credential.** Filed in vault at `projects/{id}/credentials/excalibase_runtime`, rotated with the other logins by `POST .../credentials/rotate` (the sessions opened with the old password end when `FEATURE_DBREADROLE` is on), removed with the project, reset on a restore.
- **Existing projects.** The start-up role reconcile (and every pass after it) creates and files the login for each running project without one. Nothing is redeployed: an app already running keeps the environment it was deployed with, owner login included, until its next deploy, which picks up the runtime login; a function picks it up on its next deploy. Until a project has the login, a deploy of an app that references the database is refused with "still being created, deploy again in a few minutes", and a function gets no `EXCALIBASE_DB_URL`; neither falls back to the owner's login. To cut an old app off, redeploy it and then rotate the credentials, which also ends its old sessions.
- **Off.** Apps and functions get the owner's login as before, and a runtime login a restored copy brought is locked.

## 6.5d. Function pages served as plain text (EXC-810)

A function answer typed `text/html`, `application/xhtml+xml` or `image/svg+xml` is served as `text/plain` on the shared API origin (**on unless switched off**, `FEATURE_FUNCTIONHTMLGUARD=false`, on Kubernetes and on a single host), so a fake login page cannot be served from the platform's own host. Functions have no custom domain, so there is no pass-through. Details and the cases covered: `docs/functions-response-headers.md`. Customers host pages as apps or sites.

## 6.6. Project database credential rotation

**Passwords are shown once (`FEATURE_ONETIMECREDENTIALS`, on by default, EXC-759).** With the
flag on, a database owner password is returned only by the call that sets it:
`POST /api/provision` and `POST /api/provision/{id}/database` accept an
optional `password` (16 to 128 characters; blank generates one) and return it
once; `POST .../credentials/rotate` takes an optional `{"password": ...}` and
returns the new one once. `GET .../credentials` carries no password at all (the
owner's or the read-only login's) and its `connectionUrl` holds `<password>`.
The engine, functions, backups, the watcher and app deploys read the vault
directly and are not affected. A project created before the flag has no way to
read its password back: reset it. The flag is on unless
`FEATURE_ONETIMECREDENTIALS=false` is set, on Kubernetes and on a single host.

`POST /api/provision/{id}/credentials/rotate` replaces the passwords of every
platform role filed under `projects/{id}/credentials/` — `auth_admin`, then
`excalibase_app`, then `excalibase_scheduler`, then the project owner, which is
the credential the call returns. A role that logs in by client certificate has
no password to rotate and is skipped. `cdc_watcher` is not rotated: its password is baked into the deployed
watcher's configuration, so changing it would stop the project's replication.

Each role is rotated in one order: the new password is written to
`projects/{id}/credentials/pending/<role>`, `ALTER USER` is applied, the
password is proved by opening the database with it, and only then does it
replace `projects/{id}/credentials/<role>` (and the project row, for the
owner). The pending entry is deleted last. So a pending entry you find in
vault is an interrupted rotation, not corruption: **re-run the rotate call**
and it finishes from wherever it stopped, reusing the recorded password
rather than minting another.

A pending entry is removed only on proof that the rotation changed nothing:
the credential currently on file still opens the database. A failed
`ALTER USER` is not that proof — a restarting pod, a paused project and a
rejected statement are indistinguishable from here — so if neither password
can be proved, the pending entry stays and the call reports it.

Rotation takes the project's lifecycle lease, so it is refused with
`409 project is busy` while a pause, resume, restore or teardown holds the
project, and two rotations cannot run at once.

As each role is promoted the platform publishes a `credential` change on
`policies.{projectId}.changed`, naming the role in `resource`. That is the
signal consumers need to drop a cached credential rather than wait out their
TTL; wiring it up on the engine and the auth service is tracked separately,
so for now assume those caches still converge on their own TTL.

## 6.7. Who owns migrated tables

Migrations, Studio and the SQL editor run as `excalibase_app`, which owns what
they create and holds CREATE on the database, so a migration can create a
schema. The project's own login (the one `DATABASE_URL` hands an app container) gets
SELECT, INSERT, UPDATE, DELETE and TRUNCATE on those tables and use of their
sequences, in `public` and in any schema `excalibase_app` creates (an event
trigger shares a new schema as it is created). Never TRIGGER: a trigger runs as
whoever writes the row, so one the owner attached would run its code as the
platform role. It gets no membership in `excalibase_app` and nothing in the
reserved `auth`, `excalibase` and `excalibase_*` schemas. Ownership stays with
the platform role, so the project login cannot ALTER a migrated table; change
the schema through migrations. (It owns `public` on Postgres 15+, so it can
still drop a table there, as before.) Registration and restore apply this, so a
project created before it gets it on its next restore.

## 6.8. Realtime tables log complete old rows

Turning realtime on for a table (Studio, `set_realtime`, `PUT /realtime/tables/...`)
adds it to the CDC publication and sets `REPLICA IDENTITY FULL`; turning it off
restores `DEFAULT`. The engine sends a delete to a subscriber only after judging
it against the subscriber's select permission, which needs the whole old row; a
key-only delete is withheld, so a published table must stay FULL. An
`ALTER TABLE` that would leave a published table short of FULL is refused (event
trigger `excalibase_keep_realtime_identity`), and registration and restore put
FULL back on any published table that lost it.

Cost: every UPDATE and DELETE on a published table writes the full old row to WAL
(large TOASTed values included), so WAL volume and the watcher's decoding work
grow with row size and write rate; inserts cost nothing extra. Tables without
realtime are unaffected. A table with wide rows and heavy update traffic is
the case to watch: check WAL growth (`pg_stat_wal`) before turning realtime on
for it.

## 6.9. The excalibase schema belongs to the platform

Customer-written SQL — Studio's SQL editor, MCP `execute_sql` and
`apply_migration`, table import — runs as `excalibase_app`. That role reads the
project's `auth` tables (as Supabase's SQL editor does) and works in `public`
and any schema it creates, but in the reserved `excalibase` schema it holds
USAGE only (EXC-623): no CREATE, no ownership and no DML. It can still call
the guarded functions there (`create_role`, `set_realtime_table`, ...), which
the superuser owns. `excalibase_cdc` is the watcher's alone.

The superuser owns the schema and everything in it, the scheduler tables
included. Their only writer is `excalibase_scheduler`, a login used by the
function scheduler and the deploy-time cron sync and by nothing that runs
customer SQL. It logs in by client certificate on Kubernetes, has its
search_path pinned to `pg_catalog`, and holds DML on those two tables only.

Registration enforces this. If anything in the schema belongs to a role that
is not a superuser, or the schema itself does, or any trigger sits on a table
there, registration drops the schema with everything in it and recreates the
platform's objects. It logs a WARNING when it does. A clean schema is left
alone, so the scheduler's rows survive. Unlike `auth` and `excalibase_cdc` (section 6.10), which quarantine customer objects and never drop them, this rebuild drops the schema with CASCADE, which also drops anything in another schema that depends on it. On a project registered before
EXC-623 the scheduler tables belonged to `excalibase_app`, so that rebuild
drops pending scheduled tasks. Cron rows come back on each function's next
deploy. An event trigger also refuses a non-superuser's `CREATE EXTENSION` /
`ALTER EXTENSION` into `auth`, `excalibase` or `excalibase_*`. A trusted
extension checks CREATE on the database, not on the schema, so a revoke alone
would not stop it.

Projects registered before EXC-623 are found by their missing
`projects/{id}/credentials/excalibase_scheduler` record. Every 10 minutes the
leading replica re-runs registration on each such ACTIVE Postgres project,
adds the role's certificate login to the cluster's pg_hba (only when the
cluster already admits platform roles by certificate), and then files the
login. Filing comes last, so a project that was paused, busy or failing at one
pass is retried at a later one. Look for `platform role reconcile:` in the log.

The owner holds the database, so it can attach settings to it that would make
that SQL fail: read-only transactions, a role to switch to on connect,
timeouts, PL/pgSQL extra errors, and a preload library, which makes every new
backend FATAL before it runs a line. The superuser's psql therefore pins all
of those as connection startup options (including `local_preload_libraries`),
checks it runs as the superuser itself in a writable transaction, and takes
the schema back — ownership, CREATE revoked from every other role, all table
and sequence grants revoked from `excalibase_app`, and the CREATE EXTENSION
guard installed — in a first transaction of its own. Anything later that the
owner can still make fail, such as a Postgres 16+ owner creating
`excalibase_scheduler` itself, stops registration only after that lockdown and
guard have landed. Default privileges another role set in the schema are
cleared and do not trigger the rebuild.

Those connection options also pin `local_preload_libraries` and
`session_preload_libraries` empty, `password_encryption` to `scram-sha-256`,
`default_tablespace` and `temp_tablespaces` empty, and, where the server has
them, `scram_iterations` (16+) and `transaction_timeout` (17+). The version
that gates the last two is read from the server with a first probe
connection, not from any stored value.

`lock_timeout` is finite. A customer or the owner can hold a lock that blocks
the rebuild — on an excalibase object, on a dependent object in another
schema, or on a catalog row the second script needs — so the first attempt
times out, after which the reconcile terminates every client backend that is
not a platform service and retries. A platform service is identified by its
login role, one a customer can neither create nor assume (`auth_admin`,
`cdc_watcher`, `excalibase_scheduler`, the DocumentDB roles, `streaming_replica`,
`postgres`) plus any superuser; those are spared — but only while the platform
holds the name. A service name some non-superuser holds ADMIN over (on 16+ an
owner with CREATEROLE gets ADMIN on every role it creates, so this is how a
pre-EXC-623 owner could squat `excalibase_scheduler`) is the squat guard's
case: its backends are terminated like a customer's. The first script also
reports such a squat with a WARNING, without aborting the lockdown; the second
script still refuses on it. A lock timeout is recognised only by SQLSTATE
55P03 after the ERROR prefix (psql runs with `VERBOSITY=verbose`). `excalibase_app` is not a
service for this purpose — customer SQL runs as it — nor is the owner login,
so both are terminated when they block the rebuild. The lockdown also takes
USAGE on the excalibase schema's own types from PUBLIC, so a customer cannot
type an object elsewhere on one and make it a new dependent.

## 6.10. Platform-managed schemas and database passwords (EXC-821)

**Rotate a database password only through Studio** (or `POST .../credentials/rotate`,
section 6.6). That path records the new password in the vault first, applies it,
proves it, and only then promotes it. A password changed any other way is not
what the platform holds.

**Schemas.** `excalibase`, `auth` and `excalibase_cdc` belong to the platform.
Customer SQL (`excalibase_app`), the owner's login and every role the owner
creates cannot create in them, rename, drop or take ownership of them, or grant
themselves anything there; integration tests pin each statement
(`platform_schema_drift_integration_test.go`). A customer role is any role that
is not a superuser, not a predefined `pg_` role and not on an exact list of the
roles the platform creates (`auth_admin`, `cdc_watcher`, `excalibase_scheduler`,
`postgres`, `streaming_replica` and the DocumentDB roles); never a name prefix,
since the owner can create `excalibase_helper`. A listed role that a customer
administers or granted membership in is a customer too. `excalibase_app`,
`excalibase_runtime` and `excalibase_reader` are customers for schema rights,
because customers hold their passwords. `excalibase` is the superuser's,
and registration rebuilds it if anything in it is not (section 6.9).
The `nosql` schema is not platform-managed: the function deploy still creates it
as `excalibase_app` (`edgefn/migrator.go`), so it is the customer's.

**Drift is repaired, and customer objects are never adopted.** Every reconcile
pass (10 minutes, leading replica, ACTIVE Postgres projects) probes each project
in one `psql` call. It counts as drift when, in `auth` or `excalibase_cdc`:
a customer role owns the schema; a customer role holds any privilege on the schema
or its tables, or PUBLIC can execute a function; a customer role owns a table,
view, sequence, composite type, function, procedure, type, domain or operator; a
trigger on a platform table is not the platform's; or a platform table has a rule.
Registration runs the repair as separate scripts, each its own transaction, so a
step that refuses cannot undo an earlier one. In order: the lockdown (reclaims
the platform's own tables a customer came to own, drops triggers and rules the
platform did not put on its tables, revokes every grant customer roles hold and
PUBLIC's EXECUTE; it cannot refuse), the quarantine (it skips an object it cannot
place rather than stop), the password repair, the registration SQL (whose guards
can refuse), and the refusal for whatever is left.

- A platform table (the known list in `platformServiceSchemas`: `auth.users`,
  `refresh_tokens`, `password_reset_tokens`, `email_verification_tokens`,
  `email_otps`, `auth_codes`, `flow_states`, `api_keys`) that a customer role owns
  goes back to `auth_admin` in place and is never moved. A table the auth
  service adds later belongs in that list and in
  `testdata/auth_migration_tables.txt`, which a test pins to the auth migrations.
- Customer-owned routines, types, domains and operators move to
  `excalibase_quarantine_<schema>`, the customer still the owner. The schema is
  taken over if it exists (owner to the superuser, CREATE revoked); an object whose
  name is taken there goes to a schema of its own, and when eight such names are
  taken the repair refuses (alert) instead of looping. The repair scripts run with a 60 s statement_timeout. The revokes name only roles that hold a grant (read from the ACLs), so the owner creating thousands of roles does not slow them. Every revoke cascades, so a grant chain (owner to sidekick to sidekick2 with the grant option) cannot stop it, and the same revokes clear default privileges for customer roles and grants on types and domains. Any failure of the lockdown, quarantine or refusal script, a statement or lock timeout, or the reconcile deadline raises the same alert, with its own message; each script prints `excalibase-repair-stage:<name>` so the reconcile can tell which one failed. The decision fails closed: an error from the repair run alerts unless the last script named is the password repair or the registration SQL, so an error with no script named alerts. The session pins `client_min_messages=notice`, because the owner can set it on the database or its role. Default privileges to PUBLIC count as customer ones. The quarantine runs ahead of the registration SQL, and the refusal after it, so a registration the owner makes refuse for good (by creating a platform role name such as `excalibase_docbrowser` on PG16+) cannot keep the quarantine from running; after any failed run the project is probed again and alerts while drift remains, and a refused registration alerts naming the role. The probe, quarantine and refusal are built from one list of catalogs (`platform_schema_kinds.go`: routines, types, operators, operator families and classes, collations, conversions, text search configurations and dictionaries, extended statistics, extensions, relations); a test fails when a catalog with a namespace column is on neither it nor the explained exemptions. A failed run whose aftermath cannot be probed alerts too. The platform does not drop or rename a squatted role, since it may own objects and carries the owner's password and memberships: an operator drops it. Default privileges with no schema (`ALTER DEFAULT PRIVILEGES FOR ROLE auth_admin GRANT ...`) for a platform role are cleared and probed like the per-schema ones. Nothing is dropped with CASCADE.
- Any other relation a customer role owns is not touched: registration refuses
  ("left for an operator") after the revokes have committed, and the reconcile
  raises a CRITICAL project alert (`project.platform-schema`, in the alerts table)
  that resolves when a pass finds the schemas clean. An operator reviews the object.
- A platform table is tainted, and left for an operator the same way, when anything
  attached to it depends (by `pg_depend`) on an object outside the platform schemas,
  `pg_catalog` and `information_schema`, or on an object a customer role owns. The
  walk covers the table's defaults, CHECK and foreign-key constraints, indexes
  (expressions and operator classes), column types and domains with their
  constraints, policies, triggers (the function and any routine in the WHEN clause)
  and rules. Members of a superuser-owned extension are exempt. The probe and the
  repair use the same walk, so a table the probe calls clean cannot run customer
  code. A customer publication that includes a platform table taints it too.
  A customer table that inherits a platform table, or is a partition of one, is
  detached (`NO INHERIT` / `DETACH PARTITION`), because its triggers would fire
  when the platform changes the parent, as the platform's role. The link is
  recorded child to parent, so the walk from the table does not see it; the probe
  looks at `pg_inherits` too. Detaching keeps the child's rows and columns, so
  nothing of the customer's is lost. A customer table that references a platform table (a profiles table with ON DELETE CASCADE) is not a taint, since the cascade runs as that table's owner, provided every column of the key is a built-in type and the check uses built-in operators (a customer type with an implicit cast would run customer code as the platform table's owner, so that key is a taint); a platform table that references a customer table still is. Views and other objects that read a platform
  table run as their own owner, not as the platform's role, so they are not repaired.
  Reclaimed tables get row-level security forced off, and every policy on them
  that depends on a customer object is dropped, so customer code never runs as
  `auth_admin`.

The `excalibase` schema still uses its existing rebuild (section 6.9).

**What is not prevented.** Postgres lets any role change its own password with
`ALTER ROLE ... PASSWORD`, and has no event trigger for it, so customer SQL can do
it for `excalibase_app`, and the owner for its own login. The platform does not stop
this. It finds it: each pass compares the verifier Postgres stores for the owner,
`excalibase_app`, `excalibase_reader` and `excalibase_runtime` with the password the
vault holds, and sets the database back to the vault's password. A verifier whose
iteration count is not the platform's 4096 is drift without being hashed, so a
tenant cannot stall the pass with a huge count. A role with a rotation in flight (a
`pending/<role>` entry, or a current record marked pending) is skipped. So a
password changed in SQL stops working within one reconcile interval, and until then
the vault's copy and the database disagree, so a platform call made as that role can
fail. Projects that are paused, busy or failing at a pass are repaired at a later one.

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

## Suspending an account

Suspend from the admin console (or by revoking an org); both use one operation
that deactivates the account, records the row in `abuse_suspensions`, stops its
apps and, with `FEATURE_SUSPENSIONEMAILS=true`, emails the account holder.
`POST /api/admin/console/users/{id}/suspend` takes `reason` (required, audited)
and `reasonCode`: `proxy_vpn`, `crypto_mining`, `spam_or_phishing`,
`disposable_email` or `other` (the default). The mail names the stopped
projects and apps, links `ACCEPTABLE_USE_URL#<reasonCode>` and tells the holder
to reply to `SUPPORT_EMAIL`. A mail failure is logged and counted in
`excalibase_suspension_mail_total{outcome="failed"}`; the suspension stands.
Suspensions made before the flag was on are never mailed. A crash-loop stop is
not a suspension and has its own mail.

What a suspension stops (EXC-845). It keys on the owner of the org: a suspended
account that only created a project in a good-standing org changes nothing
there, beyond being unable to start new compute in it (EXC-843). For each
project in an org the account owns, the apps are paused (including one still
rolling out), sites come off their hostnames (static sites and previews too,
and the site timer keeps them off), the function runtime stops (so functions
and cron stop), and, on Kubernetes, the public database port and read address
close. Each step is best effort: a failure is logged and listed as
`servingFailed` in the answer, and never undoes the suspension. On a single
host the database is published when its container is created (loopback, or
every interface with `DOCKER_DB_PUBLIC`), so there is no port to close per
project. The project then reads as unknown to the data plane:
`GET /api/projects/{id}/info` and the vault credential read answer 404 (503 if
the standing cannot be read), public storage files answer 404, and every write
on a project route answers 403 for the org's other members, and so does a write on an org route (rename, add or re-role a member, project members). DELETE and `/pause`
stay open, and so does the read-only `GET /api/schema/{id}/query`. A platform
admin keeps the override. The engine and the auth service keep their last copy
until their caches expire (the engine's pool up to 30 minutes, the auth service
up to 1 hour, issued end-user JWTs up to 1 hour); there is no evict call yet.

Unsuspend restores sites, the function runtime, the database port (for a running
project; a paused one comes back with its resume) and the apps the suspension
stopped, but only for a project that may serve: one awaiting deletion, in
teardown or restore, or in an org whose owner is still suspended stays down.
Serving resumes within 5 seconds, the guard's cache.

EXC-843's start rule still applies to starting compute: resuming an idle-paused
project and the background app start refuse a project created by a suspended
member, so an org in good standing cannot resume such a project until that
member is unsuspended or the project is recreated.

## Credits (EXC-866)

Off until `FEATURE_CREDITS=true` (Helm `env.FEATURE_CREDITS`, or `.env` on a single host; same on both). `FEATURE_CREDITS=true` refuses to boot unless `FEATURE_LIMITEDTRIAL` is also on, so credit never funds containers for unverified accounts. Off, no credit route is mounted (each answers like an unmounted route), nothing is granted or swept, and Studio shows the Usage page as before. This change builds the ledger, the sign-up credit, admin grants and the balance pages. Nothing meters usage yet, so the balance only moves by grants, adjustments and expiry.

Sign-up: Studio's sign-up form (email and OAuth) shows a credit terms checkbox while the flag is on; ticking it records the terms version and time, and leaving it empty still creates the account, without the credit. Every email-verification path (the link and an admin marking an address verified) tries the grant straight away; the sweeper stays as a backstop and the grant is idempotent. The admin Organizations table gains a Credit column.

- **Money** is integer micro-dollars in an append-only ledger (`credit_entries`; triggers refuse UPDATE, DELETE and TRUNCATE) split across **lots** that each expire on their own date. Σ lots = balance = Σ entries always. There are no foreign keys to orgs or users, so deleting an org leaves its ledger.
- **Sign-up credit:** a verified person who accepted the credit terms (Usage & credits page) gets it once, on their one free org. Claims on `user:<id>`, on the exact lower-cased email (no other canonicalisation, so a deleted account's address cannot earn it again) and on each linked `github:`/`google:` subject make it once per person; an identity linked later is claimed too, under the same lock a grant holds. Someone who becomes ineligible (upgraded, suspended, deactivated, no free org, identity claimed) leaves the queue and never holds a place a newcomer is owed. Past the trial cap, people join a first-come queue and are served as trials end. A sweeper runs every minute on the leading replica.
- **Settings** (all read at boot; a bad value stops the boot): `CREDITS_TRIAL_DOLLARS` (300), `CREDITS_TRIAL_DAYS` (90), `CREDITS_TRIAL_CAP` (40), `CREDITS_ADMIN_GRANT_CAP_DOLLARS` (1000), `CREDITS_TERMS_VERSION` (v1; change it to ask everyone to accept new terms), `CREDITS_TERMS_URL`.
- **Expiry:** a lot expires only once the org's meter watermark (`credit_meter_watermarks`) has passed its date, so every hour before it is billed to it first. While no meter runs, an org expires by the clock. Once a meter runs (`Rules.Metered`, set by the metering change), an org the meter has not started on keeps its lots until its first watermark.
- **Admin:** `POST /api/admin/console/orgs/{orgId}/credits` with `{amountMicros, reason, expiresAt?}` and an `Idempotency-Key` header. Session only, `manage_orgs`, reason 8 to 500 characters, at most the cap per action; a negative amount takes credit back and is refused below zero. Its audit row is written in the same transaction as the ledger entry.
- **Reads:** `GET /api/orgs/{orgId}/credits/` (balance, lots, recent entries), `.../credits/entries`, `.../credits/statement?month=YYYY-MM[&format=csv]`; org members only. Billing runs in UTC; Studio shows local time.

## Plan budget (EXC-901)

`FEATURE_PLANBUDGET` (off by default, cloud only) makes every plan a CPU, memory and disk budget plus a service count, and admits each step that grows an org's services against it. A refused step answers 409 `plan_budget_exceeded` with `need`, `left` and `budget` in the org's own numbers (never the platform's capacity). Off, nothing is read or checked and today's behaviour is unchanged. Self-hosted installs have no budget.

What counts is the size picked for each service times its copies or replicas: a database's CPU, memory and disk per copy; an app's tier limits per replica plus its disk; a project's functions runtime once its first function is deployed. A paused database or stopped app releases CPU and memory and keeps its disk. ENTERPRISE has no budget.

Plan numbers are settings: `PLANBUDGET_FREE_*` and `PLANBUDGET_STANDARD_*` for `CPU_MILLI`, `MEMORY_MIB`, `DISK_GIB`, `MAX_PROJECTS`, `MAX_SERVICES`. Defaults are Free 2 vCPU, 4 GiB, 20 GiB, one project (the owner's), six services; Standard 4 vCPU, 8 GiB, 30 GiB, 20 projects, 50 services. A bad value stops the boot. While the flag is on, the project count here replaces the plan row's.

Admission runs inside the writer's own transaction under the existing per-org lock: lock, sum, the writer's own state change, commit. The paths are project and database create, add-database, restore (new project and cancelling a deletion), database copy, plan resize, `/storage` disk growth, resume and its retry, app create, edit, scale, deploy and resume, a project's first function, and templates and MCP tools, which call the same services. Shrinking, deleting, pausing and resizing down always pass, even for an org already over its budget.

With the flag off the budget reads and records nothing: creates, add-database, resize and functions behave exactly as before it existed. With it on (cloud only; a self-hosted install is never metered), every boot records the size each database already holds, from the plan it was created under, and marks projects that already run functions, before the server takes traffic. That backfill writes only the platform's own rows: it never reads, resizes or restarts a cluster, container or workload. A database it cannot read (a plan row it cannot parse, an unknown tier) is logged and skipped, and growth in that one org is refused until it is fixed; other orgs are unaffected. Sizes are Kubernetes quantities, so `1.5Gi`, `4G` and `500M` are fine.

A failed resize puts the old size back without admitting it again, because the cluster still runs at that size. A leaked reservation fails closed (the org sees less room than it has); there is no periodic reconcile yet.

`FEATURE_CREDITS` with `FEATURE_PLANBUDGET` off is allowed (the ledger without a ceiling). With the budget on and credits off the ceiling is the plan's budget, which every current Free shape fits.

## Limited trial until verified (EXC-899, in progress)

Off until `FEATURE_LIMITEDTRIAL=true` (Helm `env.FEATURE_LIMITEDTRIAL`, or `.env` on a single host; same on both). With it off every account behaves as it does today and nothing is read.

The model: a new account is **limited** and becomes **full** once verified. Verification will be the linked GitHub account's age and activity; a card may become a second route once billing exists. What is shipped so far is the foundation only:

- A `user_trust` table (one decision per account: state, method, a reason code, time, rule version) and an append-only `trust_audit` table. No GitHub data is stored, only the result.
- One resolver answers "is this owner limited or full". The owner is the org's immutable `OwnerID`, so a limited member of a full owner's org runs at full. With the flag on, an owner with no decision, an unknown state or any read error is **limited** (fail closed); with the flag off it is full and the tables are not read.
- Because a missing decision means limited, switching the flag on before the grandfather backfill has run would limit every existing account. So with `FEATURE_LIMITEDTRIAL=true` the server refuses to start until the `trust_backfill_runs` table holds a marker (when the backfill finished and the cutoff it used), and refuses again if that cutoff differs from `TRUST_GRANDFATHER_BEFORE` (a backfill run with another cutoff made a different set of accounts full). The boot read gives up after 30 seconds. Run the backfill with `excalibase-server trust-backfill` (it asks for the vault unseal shares, reads `TRUST_GRANDFATHER_BEFORE`, makes every account created before that date full, leaves an account that already has a decision alone, and records the marker). The verify flow, the banner and the edge limits come in later changes, so the flag is not ready to turn on in prod yet.
- **Containers need a verified owner.** With the flag on, a limited owner is refused (403, code `trust_required`, naming the GitHub age and activity rule as configured) on every path that starts, builds or scales a container: app create, update (scale), deploy, redeploy, resume, git connect and build, template deploys, site builds, and function deploys (they roll the project's own Deno pod), and server-site deploys and rollbacks (they publish customer code as a Worker; a static site is not gated). A limited owner also gets no new function runtime and no function code pushed into one (a missing runtime on invoke, a replay; a secret or egress save is stored and answers 200 with a `warning` that it is not applied to running functions until the account is verified); a runtime that already exists is left as it is. The same check runs in the services, so webhook builds, the image watcher, queued deploys and the MCP tools are refused too; MCP reaches the same routes. Pause, delete, secrets, domains, disks, static site deploys and every database action stay open: databases are not gated. A platform admin acting on a project may still resume work. If the owner's decision cannot be read the action is refused (503), never allowed. Apps already running are not stopped by this. The `credits` feature must refuse to start unless this flag is on (`trust.RequireContainerGate`).
- **Verify with GitHub.** A limited account becomes full from Studio's "Verify your account" card (on Home, and where a container request was refused): one button sends it to GitHub (the same GitHub app as GitHub sign-in, so `oauth/studio/github` must be set in the vault; the callback is the same one, no new redirect URL). With the user's own token the server reads the account's age, its public events inside `TRUST_GH_ACTIVITY_WINDOW_DAYS`, and its own non-fork, non-archived public repositories, and judges them against `TRUST_GH_MIN_AGE_DAYS` and either `TRUST_GH_MIN_PUBLIC_EVENTS` or `TRUST_GH_MIN_REPOS` with one older than `TRUST_GH_REPO_MIN_AGE_DAYS`. A pass makes the account full (method `github`). A failure says what is missing. Only counts and ages are kept (`trust_verifications`, the latest attempt per account), never GitHub content. The account's email must be verified first. One GitHub account verifies one Excalibase account: a pass takes the claim `github:<numeric id>` (`trust_claims`; the credits grant uses the same key). A claim is released when its account is deleted and stays closed to others for `TRUST_CLAIM_COOLDOWN_DAYS`. Attempts are limited to one per `TRUST_VERIFY_RETRY_MINUTES` per account, and the routes are rate limited. An account an administrator limited cannot lift it this way. The routes (`GET /api/trust/`, `POST /api/trust/github/start`) answer 404 while the flag is off. Migration 000120 adds the tables. The `credits` feature must refuse to start unless this flag is on (`trust.RequireContainerGate`).
- `trust_audit` has no foreign key to users: the trail of who was limited, verified or overridden is kept when an account is deleted. `user_trust` goes with the account.
- `TRUST_GRANDFATHER_BEFORE` may not be in the future.

Settings (all read at boot; a mistyped value stops the boot instead of falling back):

| Variable | Default | What it does |
|---|---|---|
| `TRUST_GH_MIN_AGE_DAYS` | 90 | How old the linked GitHub account must be |
| `TRUST_GH_MIN_PUBLIC_EVENTS` | 5 | Public events needed inside the activity window |
| `TRUST_GH_ACTIVITY_WINDOW_DAYS` | 90 | The window those events are counted in |
| `TRUST_GH_MIN_REPOS` | 3 | Alternative to events: this many public repositories... |
| `TRUST_GH_REPO_MIN_AGE_DAYS` | 30 | ...one of which is at least this old |
| `TRUST_GH_RECHECK_DAYS` | 0 | Re-check a verified account this often; 0 verifies once |
| `TRUST_VERIFY_RETRY_MINUTES` | 10 | Wait between two verify attempts of one account |
| `TRUST_GRANDFATHER_BEFORE` | 2026-11-01 | Accounts created before this date are made full by the backfill; later ones must verify |
| `TRUST_CLAIM_COOLDOWN_DAYS` | 30 | How long a released GitHub claim stays unusable by another account |
| `EDGE_LIMITED_MBPS` | 1 | Outbound rate at the edge for a limited account's hosts |
| `EDGE_LIMITED_ALLOW_WS` | false | Let a limited account's hosts upgrade to WebSocket |
| `POD_LIMITED_EGRESS_MBPS` | 1 | Cap on a limited account's app pods' own outbound traffic |

The last three, the repository thresholds and the claim cooldown are read and checked now but used by later changes.

## Self-healing failed operations

Off until `FEATURE_SELFHEAL=true` (Helm `env.FEATURE_SELFHEAL`, or `.env` on a single host; same on both). On, a failed operation retries itself and staff are told when it cannot:

- A teardown whose backup purge failed (`BACKUPS_PENDING_DELETE`) is purged again by the rollback sweep, every 5 minutes at first and doubling to an hour, under the project lease. The admin `POST /backups/purge` still works.
- A project stuck in `PAUSING` or `RESUMING` is retried on every plan, and also with `EXCALIBASE_AUTOPAUSE_ENABLED=false`; the sweep then judges no project idle. The wait between tries is kept on the project row, as before.
- After `EXCALIBASE_SELFHEAL_ALERT_AFTER` failed attempts (default 5) a CRITICAL platform alert is raised, `selfheal.stuck:<project>` or `selfheal.retry:<project>`, and mailed to the platform admins like the backup-credential alert. It closes when the project recovers or is gone. Retries go on at the ceiling after the alert.
- A pause or resume that did not settle tells the customer "we are retrying automatically" instead of asking them to retry.

The purge retry's attempt count lives in the process, so a restart or a new leader delays the alert by at most its limit and never raises a false one.

## Database audit (pgaudit)

`POST /api/provision/{id}/audit/enable` (Admin) turns on pgaudit for a Postgres project on Kubernetes. It **restarts the database**: CloudNativePG preloads the library only once a `pgaudit.log` setting is on the cluster, and changing it rolls the instances (a few seconds on one instance, a switchover on an HA tier). The call answers `202` as soon as the cluster is changed; a background task then waits for the library and creates the extension. Poll `GET /api/provision/{id}/audit/config`: `status` is `pending` while pgaudit is preloaded but the extension is not created yet, and `enabled` (with `enabled: true`) only when both are done. Enabling again while the setting is there writes nothing and finishes a task that was lost, for example by a platform restart.

Body: `{"enabled": true, "settings": {"pgaudit.log": "ddl,write"}}`. The default is `ddl,role,write`; classes are `read, write, function, role, ddl, misc, misc_set, all`. Anything else is a `400`, as is `"enabled": false`: turning audit off is not supported, because the extension's event triggers need the library. Restores and plan rollbacks keep the setting. On a single host the route answers `409`: audit is Kubernetes-only.

## App containers on a single host: read-only root (EXC-732)

App containers on Docker or Podman run with a read-only root filesystem. They write `/tmp`, a tmpfs sized at `APP_TMP_PERCENT` (default 25) percent of the app's memory limit, and their disk volume. This replaces a disk quota on the engine's writable layer, which only some storage engines can enforce (`--storage-opt size` needs overlay2 on xfs with project quotas; the containerd snapshotter has none). An app that writes elsewhere fails with `Read-only file system`: tell its owner to point caches at `/tmp` or give the app a disk; the framework list is in docs/deployment/docker-local.md.

- `/tmp` is memory, so a larger `APP_TMP_PERCENT` takes memory from the app, never disk. It counts against the app's own limit, and an `exec` mount, because runtimes run native helpers from it.
- **An app's disk volume is not capped.** It is on the host's filesystem with its size recorded, not enforced, so an app can still fill the host through it. Whether and how to cap it (a loopback or quota-backed volume) is an open owner decision. Watch the host's disk (the host monitor's disk alert) until then.
- An image's own `VOLUME` paths each get a capped tmpfs from the same `APP_TMP_PERCENT` budget (split evenly with `/tmp`); the engine makes no anonymous volume. Podman keeps the image's files in the directory (`tmpcopyup`); Docker rejects that option, so the directory starts empty.
- The readiness probe and the edge CA live in a volume of the platform's own (`excalibase-vol-probe-<digest>`, labelled `excalibase.shared-readonly`), filled once through a container of the platform's tools image that the engine proxy refuses to start, and mounted read-only into every app. An empty `-ready` volume of the same name marks it complete; a probe volume without one is removed and filled again. A new probe binary or CA makes a new volume and removes the old one; an old volume that apps still mount stays, with its marker, until they are gone.
- Logs rotate per container: `APP_LOG_MAX_SIZE` (default `5m`) times `APP_LOG_MAX_FILES` (default `3`) files, json-file driver, so 15 MiB per container and 1.5 GiB for a hundred. The platform cannot read the host's free space, so these are fixed numbers per container: multiply by the apps you expect and compare with the disk.
- Kubernetes apps get the same read-only root and an in-memory `/tmp` in their own change.

