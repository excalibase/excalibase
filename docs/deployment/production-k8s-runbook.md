# Production runbook: platform-aio on Kubernetes

Order of operations for installing and running the Excalibase control plane
(`charts/platform-aio` in the `excalibase-service` repo) on a production
Kubernetes cluster. Target: RKE2 (CIS-hardened) with HAProxy at the edge,
CloudNativePG (CNPG) for every Postgres, Cloudflare R2 for backups, Resend for
email. No AWS dependency: KMS auto-unseal exists in the chart but is optional
and deferred.

Every command and values key below exists in the repos today, with its source
file named next to it; pending work is marked with its `EXC-` ticket. API-level
operations (PATs, capacity, logs, drop project, revoke org) live in
[OPERATOR.md](../../OPERATOR.md).

## 0. Checklist (copy-paste order)

```
[ ] Cluster: Kubernetes >= 1.30, default StorageClass (RWO), enforcing CNI
[ ] CNPG operator 1.23.0 installed (cnpg-system)                        -> §1.2
[ ] kube-prometheus-stack CRDs present (or serviceMonitors.enabled=false) -> §1.4
[ ] cert-manager + ClusterIssuer letsencrypt-prod, DNS for api.* and admin.* -> §1.5
[ ] Namespace excalibase-platform created                               -> §2
[ ] Secret r2-creds        (access_key_id, secret_access_key, endpoint, bucket, region)
[ ] Secret resend-creds    (api-key)
[ ] values-prod.yaml copied and edited: image tags, hosts, cors.origins, admin ingress
[ ] helm upgrade --install platform ... -f values-prod.yaml   (NO --wait on first install)
[ ] kubectl wait job/platform-bootstrap complete; read admin-pass + provisioning-pat
[ ] Log in as admin, create a real platform_admin, mint an operator PAT     -> §3.4
[ ] Replace the 12h provisioning-pat with a durable service PAT, restart auth+graphql -> §3.4
[ ] PUT /api/admin/tiers/{FREE,STANDARD,ENTERPRISE} for your node sizes     -> §3.5
[ ] provisioning.registrationMode=invite before exposing the admin host     -> §3.6
[ ] Run tests/e2e-restore-drill.sh once against the live cluster             -> §4.2
[ ] alerting.enabled=true + webhookUrl; confirm PrometheusRule loaded         -> §4.7
[ ] Record: image tags deployed, R2 key ids, PAT expiry dates                 -> §4.4
```

## 1. Prerequisites

### 1.1 Cluster

| Requirement | Why | Source |
|---|---|---|
| Kubernetes >= 1.30 | graphql pod uses the native `lifecycle.preStop.sleep` action | `charts/platform-aio/templates/graphql.yaml` ("Requires k8s >= 1.30") |
| A default StorageClass supporting RWO | CNPG data volumes (platform-db + every tenant) and the optional `provisioning-data` PVC | `values.yaml` `provisioning.persistence.storageClass: ""` (empty = cluster default); tenant clusters take `storageClassName` from the provision request (`server-go/internal/domain/dto.go`) |
| An enforcing CNI (Calico / Cilium) | per-project default-deny ingress policy (EXC-325) and the Deno egress NetworkPolicy are no-ops on a CNI that ignores NetworkPolicy | `server-go/internal/k8s/client.go` `ensureNamespaceIsolationPolicy`; OPERATOR.md §6.1 |
| Nodes sized for the tiers you enable | provisioning refuses a project that does not fit `Allocatable - headroom` | §3.5, `CAPACITY_HEADROOM_PERCENT` |
| `kubectl` >= 1.30, `helm` >= 3.15 | versions the nightly install job runs with | `.github/workflows/aio-e2e.yml` |

The chart's pods run non-root, all capabilities dropped,
`seccompProfile: RuntimeDefault` (`templates/provisioning.yaml`, `auth.yaml`),
so RKE2's CIS profile needs no PodSecurity exemption for the platform
namespace. The bootstrap Job (`alpine:3.20`) runs `apk add curl jq openssl` at
runtime (`templates/bootstrap-job.yaml`): allow egress to the Alpine mirrors
or mirror the image. metrics-server is **not** read by the platform code (no
`metrics.k8s.io` client in `server-go/`); it is only needed once an HPA
exists (EXC-50, pending).

### 1.2 CloudNativePG operator (pinned)

Every install path in both repos pins the same manifest:

```bash
# charts/background/cnpg-operator/install.sh and .github/workflows/aio-e2e.yml
kubectl apply --server-side --force-conflicts -f \
  https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.23/releases/cnpg-1.23.0.yaml
kubectl wait --for=condition=available --timeout=180s \
  deployment/cnpg-controller-manager -n cnpg-system
```

`--server-side` is required: the `poolers` CRD exceeds the client-side
annotation limit (comment in `aio-e2e.yml`). Pause/resume needs CNPG >= 1.20
(declarative hibernation, OPERATOR.md §6.3). Check the upstream CNPG
compatibility matrix for the 1.23 line against your RKE2 Kubernetes version
before upgrading either side.

### 1.3 HAProxy at the edge, ingress class

The chart renders `networking.k8s.io/v1` Ingress objects with
`ingressClassName: {{ .Values.ingress.className }}` (default `nginx`) and
**nginx-specific** annotations: `use-regex`, `proxy-read-timeout`,
`limit-rps`, `limit-connections`, `whitelist-source-range`
(`templates/ingress.yaml`). With HAProxy in front of an in-cluster
ingress-nginx nothing changes: keep `ingress.className: nginx` and point
`ingress.host` / `ingress.admin.host` at the names HAProxy forwards. With
HAProxy as the Ingress controller itself, the regex paths `/[^/]+/graphql`,
`/[^/]+/api/v1` (`pathType: ImplementationSpecific`) and the source-IP
allowlist annotation are not honoured; there is no HAProxy variant of the
template and no ticket found, so the control-plane allowlist must then live
in HAProxy.

The public ingress carries only the data plane: `/{projectId}/graphql`
(+ WebSocket), `/{projectId}/api/v1` (REST, served by the graphql pod,
EXC-350), `/auth`, `/functions/v1`. The control plane (`/api` + Studio) is a
separate Ingress (`ingress.admin.*`), off by default, and must sit behind a
VPN or allowlist (`ingress.admin.allowCidrs`).

### 1.4 Prometheus operator CRDs

`observability.serviceMonitors.enabled: true` (default) renders
`ServiceMonitor`, `PodMonitor` and `PrometheusRule` objects
(`templates/servicemonitors.yaml`, `templates/prometheusrules.yaml`). The
install fails on a cluster without those CRDs. Either install
kube-prometheus-stack first (the labels expect `release: monitoring`) or set
`--set observability.serviceMonitors.enabled=false`.

```bash
# scripts/install-all.sh, step B
helm upgrade --install monitoring prometheus-community/kube-prometheus-stack \
  -n monitoring --create-namespace \
  -f charts/background/monitoring/values-local.yaml --wait --timeout 10m
helm upgrade --install loki grafana/loki-stack -n monitoring \
  -f charts/background/monitoring/values-loki-local.yaml --wait --timeout 5m
```

`values-local.yaml` sets the Grafana admin password to `admin`: override it.
`/api/admin/logs` and the live CPU/mem columns read `LOKI_URL` / `PROM_URL`
(`provisioning.observability.lokiUrl` / `promUrl`); both degrade gracefully
when the stack is missing.

### 1.5 DNS and TLS

* Two hostnames: `ingress.host` (data plane, default `api.excalibase.io` in
  `values-prod.yaml`) and `ingress.admin.host` (control plane + Studio).
  Studio calls `/api` on the **same** origin as itself, so the admin host
  serves both (`templates/ingress.yaml`).
* TLS via cert-manager: `ingress.tls.clusterIssuer` (default
  `letsencrypt-prod`) is rendered as the `cert-manager.io/cluster-issuer`
  annotation; certificates land in `ingress.tls.secretName` and
  `ingress.admin.tls.secretName`. The `TLSCertExpiringSoon` alert reads
  cert-manager metrics.
* The `ingress.annotations:` block in `values-prod.yaml` is not rendered
  (no `.Values.ingress.annotations` in the template); extra annotations need
  a template change.
* `cors.origins` must list the Studio origin(s) exactly with
  `cors.secureCookies: "true"`; `"*"` breaks cookie login (`values-prod.yaml`).

## 2. Secrets to prepare before install

All in namespace `excalibase-platform` (`.Values.namespace`). Key names are
what `templates/provisioning.yaml` mounts; every one is `optional: true`, so a
typo does not stop the pod, it silently degrades the feature.

| Secret | Keys | Used for | Created by |
|---|---|---|---|
| `r2-creds` | `access_key_id`, `secret_access_key`, `endpoint`, `bucket`, `region` | `R2_*` env → backup store for every tenant cluster and restores (OPERATOR.md §6 "Vault uninitialised fallback") | you, before install |
| `resend-creds` | `api-key` | `RESEND_API_KEY`; requires `email.provider: resend` | you, before install |
| `deno-runtime-secret` | `secret` | `DENO_RUNTIME_SECRET` / `RUNTIME_SECRET` on every per-project Deno pod | chart, first install only (`helm.sh/resource-policy: keep`, `lookup`-preserved) |
| `platform-bootstrap` | `provisioning-pat`, `unseal-key`, `admin-pass` | vault auto-unseal, auth/graphql service PAT, first admin password | `platform-bootstrap` Job |
| `platform-db-app` | `uri`, `username`, `password` | `PLATFORM_DB_URL` | CNPG operator |
| `excalibase-api-tls`, `excalibase-admin-tls` | TLS | ingress | cert-manager |
| `ses-creds` | — | AWS SES; **not used** in this target | leave absent |
| `vault.kmsUnseal.credentialsSecret` | `access_key_id`, `secret_access_key` | KMS envelope unseal; **deferred**, leave `vault.kmsUnseal.enabled: false` | — |

```bash
# .github/workflows/aio-e2e.yml, step "Create namespace + R2/Resend secrets"
NS=excalibase-platform
kubectl create namespace "$NS"
kubectl create secret generic r2-creds -n "$NS" \
  --from-literal=access_key_id="$R2_ACCESS_KEY_ID" \
  --from-literal=secret_access_key="$R2_SECRET_ACCESS_KEY" \
  --from-literal=endpoint="https://<account_id>.r2.cloudflarestorage.com" \
  --from-literal=bucket="excalibase-backups" \
  --from-literal=region="auto"
kubectl create secret generic resend-creds -n "$NS" \
  --from-literal=api-key="$RESEND_API_KEY"
```

* `excalibase-backups` is also the code fallback (`BACKUP_DEFAULT_BUCKET`,
  `server-go/cmd/server/main.go`) and the Barman `destinationPath` prefix
  (OPERATOR.md §6); keep Secret and bucket in agreement.
* `platformDb.backup.*` in `values-prod.yaml` (schedule, retention, s3) is
  **not consumed** by `templates/platform-db.yaml`: tenant clusters are
  backed up, the control-plane DB is not (OPERATOR.md §6). The
  `--set platformDb.backup.s3.*` lines in its header are inert. No ticket found.

## 3. Install

### 3.1 Values file

Copy `charts/platform-aio/values-prod.yaml` and edit:

| Key | Set to | Note |
|---|---|---|
| `provisioning.image`, `auth.image`, `graphql.image`, `studio.image`, `provisioning.denoRuntime.image` | `excalibase/<repo>:main-<sha>` | The `v1.0.0` tags in `values-prod.yaml` do **not exist** on Docker Hub, and `excalibase/auth` is not a repository — auth is `excalibase/excalibase-auth`. CD publishes `latest`, `main` and `main-<sha>`; pin `main-<sha>` (release tags: EXC-47 / EXC-332 / EXC-346 pending). |
| `provisioning.deploymentMode` | `cloud` | §3.2 |
| `provisioning.registrationMode` | `invite` | §3.6 |
| `provisioning.publicBaseUrl` | `https://<ingress.host>` | used in SDK snippets and function invoke URLs |
| `provisioning.persistence.enabled` | `true` | otherwise `/data` is an emptyDir (NOTES.txt warning) |
| `email.provider` | `resend` | default is `ses` |
| `email.fromAddress`, `email.fromName` | your sender | |
| `ingress.enabled`, `ingress.host` | `true`, your data-plane host | |
| `ingress.admin.enabled`, `ingress.admin.host`, `ingress.admin.allowCidrs` | `true`, admin host, your CIDRs | empty `allowCidrs` = open |
| `cors.origins`, `cors.secureCookies` | admin host origin, `"true"` | |
| `platformDb.instances` | `3` (multi-node) or `1` | §5.5 for the PDB consequence |
| `alerting.enabled`, `alerting.webhookUrl` | `true`, your receiver | §4.7 |
| `vault.kmsUnseal.enabled` | `false` | deferred; no AWS |

Resource requests/limits for provisioning, auth, graphql and studio are
hard-coded in their templates; the `resources:` blocks in `values-prod.yaml`
are not rendered (verified against `templates/*.yaml`). Only
`platformDb.resources` and `waitFor.resources` are values-driven.

Not exposed as values today (must be set by editing the Deployment env after
install, and re-applied after every `helm upgrade`): `EXCALIBASE_AUTOPAUSE_ENABLED`,
`EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS`, `BYOC_EGRESS_ALLOWLIST`,
`BACKUP_DEFAULT_*`, `LOG_LEVEL` (all read in `server-go/internal/config/config.go`
or `cmd/server/main.go`; none in `templates/provisioning.yaml`). No ticket found.

### 3.2 `deploymentMode`: cloud vs selfhosted

| | `cloud` (production target) | `selfhosted` |
|---|---|---|
| Orgs | multi-org, create/delete via `/api/orgs` | one default org auto-created with the first admin |
| Tiers | `tier_configs` enforced at provision time | not enforced |
| Idle auto-pause | on by default (`EXCALIBASE_AUTOPAUSE_ENABLED` defaults to `true` in cloud) | off by default |
| Vault | Postgres-backed, Shamir-sealed, init + unseal by the bootstrap Job | same — the Job runs in every mode on k8s |
| Platform store | Postgres (`PLATFORM_DB_URL` from `platform-db-app`) | same |

Source: `docs/deployment/README.md`, `docs/idle-pause.md`. In both modes the
`platform-bootstrap` Job is the only thing that initialises and unseals the
vault on Kubernetes (`templates/bootstrap-job.yaml`, EXC-282 / EXC-360).

### 3.3 Run the install

```bash
# OPERATOR.md §1 — do NOT add --wait on a first install (see §5.4)
helm upgrade --install platform ./charts/platform-aio \
  --namespace excalibase-platform --create-namespace \
  -f my-values-prod.yaml --timeout 900s

# scripts/install-all.sh step D
kubectl -n excalibase-platform wait --for=condition=complete \
  job/platform-bootstrap --timeout=300s

# .github/workflows/aio-e2e.yml "Wait for all deployments Ready"
for d in provisioning auth graphql studio; do
  kubectl -n excalibase-platform rollout status deploy/$d --timeout=300s
done
kubectl -n excalibase-platform get pods
kubectl -n excalibase-platform get cluster      # "Cluster in healthy state"
```

Boot order is enforced by init containers (`waitFor.enabled`, EXC-54):
platform-db + NATS → provisioning → auth → graphql; the bootstrap Job waits on
provisioning `/healthz`. A pod stuck in `Init:N/M` names the missing
dependency in `kubectl logs <pod> -c wait-for-<dep>`.

What the `platform-bootstrap` Job does (`templates/bootstrap-job.yaml`,
post-install **and** post-upgrade hook, idempotent):

1. waits for provisioning `/healthz`;
2. first run: `POST /api/auth/register` user `admin` (email
   `admin@excalibase.local`, random password) — the first registration on a
   fresh platform is auto-promoted to `platform_admin`
   (`server-go/internal/handler/auth.go`); logs in to obtain a PAT;
3. `POST /api/vault/init` with `{"shares":1,"threshold":1}`, then
   `POST /api/vault/unseal`; on every later run it re-unseals if
   `GET /api/vault/status` reports `sealed: true`;
4. seeds the ES256 signing keypair at `pki/signing/{private,public}` if absent;
5. upserts the `platform-bootstrap` Secret (`provisioning-pat`, `unseal-key`,
   `admin-pass`).

Auth and graphql mount `platform-bootstrap` (`templates/auth.yaml`,
`templates/graphql.yaml`) and stay in `CreateContainerConfigError` until step
5 has run. That is expected on a first install.

### 3.4 First admin and operator PAT

```bash
# OPERATOR.md §1
kubectl -n excalibase-platform get secret platform-bootstrap \
  -o jsonpath='{.data.admin-pass}' | base64 -d ; echo
# scripts/install-all.sh
kubectl -n excalibase-platform get secret platform-bootstrap \
  -o jsonpath='{.data.provisioning-pat}' | base64 -d ; echo
```

Then, at `https://<admin host>/login` as `admin`:

1. Create a real platform admin (Studio `/admin`, or
   `POST /api/auth/users` with `{"username","email","password","role":"platform_admin"}` —
   requires `PermManageUsers`, `server-go/cmd/server/main.go`).
2. Mint an operator PAT with an expiry (`POST /api/auth/tokens`,
   `{"name":"ops","expiresIn":"30d"}`; default 90d, max 365d — OPERATOR.md §1.1).
3. **Replace the service token now.** The Job obtains `provisioning-pat` from
   `POST /api/auth/login`, which issues a `session`-scoped token with a
   **12-hour** TTL (`sessionTokenTTL` in `server-go/internal/handler/auth.go`).
   auth reads it at startup to fetch the signing key
   (`excalibase-auth/cmd/server/main.go`), graphql uses it per request for
   tenant credentials and RLS policies (`templates/graphql.yaml`). Twelve
   hours after install, end-user register answers 503 and graphql cannot
   resolve tenants — the outage and the fix are recorded in
   `excalibase-service/aio-e2e/k8s-dataplane/README.md` ("Deployment
   prerequisite discovered"). As the **real** platform admin from step 1
   (tokens die with their owner, so not the bootstrap `admin`):

   ```bash
   # OPERATOR.md §1.1 — durable PAT for the services; "never" only if you rotate on a schedule
   NEW=$(curl -sf -X POST -H "Authorization: Bearer $ADMIN_PAT" -H 'Content-Type: application/json' \
     -d '{"name":"platform-services","expiresIn":"365d"}' https://<admin host>/api/auth/tokens | jq -r .token)
   kubectl -n excalibase-platform get secret platform-bootstrap -o json \
     | jq --arg v "$(printf '%s' "$NEW" | base64 -w0)" '.data["provisioning-pat"]=$v' \
     | kubectl apply -f -
   kubectl -n excalibase-platform rollout restart deploy/auth deploy/graphql
   ```

4. Only then delete or rotate the bootstrap `admin` user. Diary the PAT
   expiry (§4.4).

### 3.5 Tiers

Tier defaults are seeded from `server-go/internal/config/tiers.go` into
`tier_configs`: FREE 0.5 CPU / 512Mi / 5Gi / 1 project / no backup /
auto-pause 7d; STANDARD 2 CPU / 4Gi / 50Gi / 5 projects / backup;
ENTERPRISE 4 CPU / 16Gi / 500Gi / unlimited / backup. All tenants are
single-instance CNPG clusters (no HA per tenant).

Resize for your nodes through the admin API (`PUT /api/admin/tiers/{tier}`,
`PermManageSetup`; `server-go/internal/handler/tier_config.go`):

```bash
# .github/workflows/aio-e2e.yml "Size FREE tier for the 2-vCPU runner"
PAT=$(kubectl get secret platform-bootstrap -n excalibase-platform -o jsonpath='{.data.provisioning-pat}' | base64 -d)
curl -sf -X PUT https://<admin host>/api/admin/tiers/FREE \
  -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"maxProjects":1,"instances":1,"storageSize":"5Gi","memory":"512Mi","cpu":"0.25","backupEnabled":false,"autoPauseAfterDays":7}'
curl -sf -H "Authorization: Bearer $PAT" https://<admin host>/api/tiers | jq
```

Scaling limit to plan around: on a 2-vCPU node the platform pods alone
request about 1310m (comment in `aio-e2e.yml`), so a 0.5-CPU FREE tenant is
refused with `provision refused: CPU`; the workflow drops FREE to 0.25 CPU.
Use `GET /api/capacity` → `tiers.<tier>.projectsCanFit` (OPERATOR.md §2)
before opening signup. Headroom is `provisioning.capacity.headroomPercent`
(15 by default).

### 3.6 Registration mode

`provisioning.registrationMode: invite` renders `REGISTRATION_MODE=invite`
(`templates/provisioning.yaml`): after the first admin, only an email with a
pending org invite (`POST /api/orgs/{id}/invite`) can register; everyone else
gets `403 registration is invite-only` (`server-go/internal/handler/auth.go`).
Set it before the admin host is reachable from anywhere you do not control.

## 4. Day-2 operations

### 4.1 Upgrades

Images ship in lockstep (OPERATOR.md §7). Bump all tags together:

```bash
helm upgrade platform ./charts/platform-aio -n excalibase-platform \
  -f my-values-prod.yaml \
  --set provisioning.image=excalibase/provisioning:main-<sha> \
  --set auth.image=excalibase/excalibase-auth:main-<sha> \
  --set graphql.image=excalibase/excalibase-graphql:main-<sha> \
  --set studio.image=excalibase/studio:main-<sha> \
  --set provisioning.denoRuntime.image=excalibase/deno-runtime:main-<sha>
```

* `helm upgrade` re-runs the idempotent bootstrap Job (post-upgrade hook);
  `--wait` is safe here because `platform-bootstrap` already exists.
  `NOTES.txt` warns for every image still on `:latest` (EXC-332).
* The per-project CDC watcher image is **not** a value: provisioning installs
  `excalibase/excalibase-watcher-go:latest` (hard-coded in
  `server-go/internal/provisioner/postgresql.go`; pinning: EXC-47 / EXC-346).
* `provisioning` is `strategy: Recreate`, `replicas: 1` (RWO volume): expect a
  short control-plane blip. graphql surges one pod so WebSocket subscriptions
  drain (`templates/graphql.yaml`).
* Never delete `deno-runtime-secret` (`helm.sh/resource-policy: keep`); a new
  value 401s every running project runtime (`templates/deno-runtime-secret.yaml`).

### 4.2 Backups and restore drill (R2)

Tenant clusters: CNPG + Barman → `s3://excalibase-backups/{projectId}/cloud/`
(OPERATOR.md §6). Credentials reach the CNPG Cluster through the per-project
`backup-s3-creds` Secret, populated from vault `backup/s3` or, when that is
absent, from the `r2-creds` env fallback. Tier `backupEnabled` decides whether
a project gets a backup at all.

```bash
# OPERATOR.md §6 CLI
curl -sf -H "Authorization: Bearer $PAT" https://<admin host>/api/provision/<projectId>/backup/list | jq
curl -X POST -H "Authorization: Bearer $PAT" https://<admin host>/api/provision/<projectId>/backup/trigger
curl -X POST -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d '{"backupId":"<id>","newProjectName":"restored"}' \
  https://<admin host>/api/provision/<projectId>/backup/restore
```

`RestoreRequest` accepts `backupId` or one of `targetTime` / `targetXid` /
`targetLsn` / `targetName`, plus the required `newProjectName` display name.
The restored project's id is generated by the platform and reported on the
restore job as `newProjectId` (`server-go/internal/domain/dto.go`).

Run the drill after install and after any change to R2 keys:

```bash
# tests/e2e-restore-drill.sh — needs port-forwards on 24005/24000 and an org with slug "acme"
kubectl -n excalibase-platform port-forward svc/provisioning 24005:24005 &
API_PROV=http://localhost:24005 bash tests/e2e-restore-drill.sh
```

It provisions a STANDARD project, writes a canary row, triggers a backup,
forces `pg_switch_wal()` (a restore started before the end-of-backup WAL is
archived fails with "WAL ends before consistent recovery point" and wedges
the target cluster — comment in the script), restores into a new project and
asserts the canary. It hard-codes org slug `acme` and leaves its R2 objects.

Rotating R2 keys: update `r2-creds` (and vault `backup/s3` if set), then walk
every project namespace and re-apply `backup-s3-creds`; there is no
auto-rotate loop (OPERATOR.md §6). Deprovision keeps backups unless
`{"confirmDeleteBackups": true}` is sent.

### 4.3 Pause / resume and idle auto-pause

Manual (Admin on the org, OPERATOR.md §6.3):

```bash
curl -sf -X POST -H "Authorization: Bearer $PAT" https://<admin host>/api/provision/<projectId>/pause | jq
curl -sf -X POST -H "Authorization: Bearer $PAT" https://<admin host>/api/provision/<projectId>/resume | jq
```

Pause takes a backup first, then sets `cnpg.io/hibernation: "on"`. The call
returns immediately but the pod stays up to `smartShutdownTimeout` (180 s)
because the CDC watcher holds a replication session, so expect ~2–3 min until
compute is actually released (EXC-363). Resume polls
`status.readyInstances >= 1` for up to 5 min; measured ~25 s.

Idle auto-pause (`docs/idle-pause.md`, EXC-279/280): hourly sweep, one leader
via advisory lock; tiers with `autoPauseAfterDays = N > 0` warn at N-1 days
(audit `project.idle_warning` + email via Resend) and pause at N days
(`pauseReason: idle`). Configure per tier through the same
`PUT /api/admin/tiers/{tier}` body (`autoPauseAfterDays`, `0` = never).
`EXCALIBASE_AUTOPAUSE_ENABLED` is the global switch (default on in cloud
mode; not a chart value, §3.1).

### 4.4 PAT expiry and rotation

Every PAT expires (default 90d, `expiresIn` up to `365d`, `"never"` only for
break-glass). Rotate with `POST /api/auth/tokens/{sha256}/rotate` and an
optional `graceSeconds` (0–3600) so a fleet can swap without a hard cut; an
expired token answers `401` with `"code":"token_expired"` (OPERATOR.md §1.1).

The service token in `platform-bootstrap` (§3.4 step 3) is owned by your
platform admin; rotate it with `graceSeconds` so auth/graphql keep working
while you patch the Secret, then `kubectl rollout restart deploy/auth
deploy/graphql`. Nothing renews it automatically: the bootstrap Job only
re-reads whatever is in the Secret (`templates/bootstrap-job.yaml`), and on
a later `helm upgrade` it re-uses that value for the re-unseal call without
checking the HTTP status (`curl -sS`, no `-f`), so an expired token makes
the re-unseal a silent no-op (§5.3). No ticket found.

### 4.5 Egress allowlists

* Edge functions: no network by default; per project
  `PUT /api/projects/{id}/functions/egress` `{"allowedHosts":[...]}` (Deno
  `--allow-net` grammar, max 64 entries, `docs/functions-egress.md`). The
  operator floor `EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS` (e.g.
  `*.excalibase.io,api.resend.com`) is unioned into every project; a
  malformed value stops the server at boot. On k8s the list is rendered as
  `ALLOWED_HOSTS` env plus a `deno-runtime-egress` NetworkPolicy
  (OPERATOR.md §6.1).
* BYOC: internal ranges and cloud metadata are always refused;
  `BYOC_EGRESS_ALLOWLIST` (CIDRs, IPs, hostnames, `*.suffix`) restricts
  targets further (OPERATOR.md §6, `server-go/internal/byoc/doc.go`).

Both env vars must be added to the provisioning Deployment by hand (§3.1).

### 4.6 PDB, HPA and scaling knobs

| Knob | Values key | Behaviour |
|---|---|---|
| Chart PDBs (nats, provisioning, studio) | `pdb.enabled`, `pdb.minAvailable` | rendered only when that service has `replicas > 1` (EXC-48); a single replica never gets one |
| platform-db PDB | `platformDb.enablePDB` | unset = on iff `platformDb.instances > 1`; CNPG creates the budgets |
| graphql replicas | `graphql.replicas` | stateless; heartbeat/drain knobs `websocketHeartbeatSeconds`, `shutdownPhaseTimeout`, `preStopSleepSeconds`, `terminationGracePeriodSeconds` |
| provisioning / nats replicas | `provisioning.replicas`, `nats.replicas` | keep at 1: RWO volume and no NATS clustering (`values.yaml` comments) |
| HPA | — | none in `platform-aio` (EXC-50 pending); the standalone `charts/{auth,graphql,rest}` and the watcher chart have `autoscaling.*` but are not used by the AIO |

### 4.7 Alerts

Rules in `templates/prometheusrules.yaml` (loaded when
`observability.serviceMonitors.enabled`): `ServiceTargetDown`, `PodNotReady`,
`GraphqlHighErrorRate`, `GoServiceHighErrorRate`, `GraphqlHighLatencyP99`,
`ProvisionOperationSlow`, `CNPGInstanceUnhealthy`, `CNPGBackupFailing`,
`ContainerCPUNearLimit`, `ContainerMemoryNearLimit`, `CNPGConnectionsNearMax`,
`PVCUsageHigh`, `CNPGReplicationLagHigh`, `CNPGReplicaWalReceiverDown`,
`CNPGReplicationSlotInactive`, `CNPGReplicationSlotRetainedWALHigh`,
`WatcherTargetDown`, `WatcherNATSPublishErrors`, `TLSCertExpiringSoon`.
Thresholds under `alerts.*`; each rule carries a `runbook_url` built from
`alerts.runbookBaseUrl` pointing at
[`charts/platform-aio/runbooks/alerts.md`](https://github.com/excalibase/excalibase-service/blob/main/charts/platform-aio/runbooks/alerts.md).

Routing is off until `alerting.enabled=true` and `alerting.webhookUrl` is set
(`templates/alertmanagerconfig.yaml`, generic webhook, label `release:
monitoring`). `GraphqlHighLatencyP99` needs `graphql.requestHistogram: true`
(default). CDC lag is not measured yet (EXC-34); `CNPGReplicationSlotRetainedWALHigh`
is the proxy.

## 5. Troubleshooting

### 5.1 Stuck project states (OPERATOR.md §6.3, §8)

| Status / symptom | Cause | Fix |
|---|---|---|
| `PAUSING` | backup ok, workload stop failed | `kubectl -n <org>-<project> describe cluster`; retry `POST /pause` |
| `RESUMING` | annotation already `off`, primary not ready in 5 min | watch `kubectl -n <ns> get pods -w`; retry `POST /resume` (idempotent). Do not read `status.phase`, only `readyInstances` + the hibernation condition |
| `BACKUPS_PENDING_DELETE` | deprovision done, R2 purge failed | `POST /api/provision/{id}/backups/purge` |
| `WAITING_FOR_READY` never completes | operator panic or PVC bind failure | `kubectl logs -n cnpg-system deployment/cnpg-controller-manager`; check StorageClass |
| `not enough capacity` | headroom exhausted | add nodes, lower tier CPU, or `provisioning.capacity.headroomPercent` |
| Deno `CrashLoopBackOff` "RUNTIME_SECRET ... required" | `deno-runtime-secret` missing | `helm upgrade` recreates it |
| Watcher "permission denied to use replication slots" | old image ordering bug | upgrade the image |
| Auth 503 "failed to connect to project database" | stale vault path | `kubectl rollout restart deploy/auth` |

### 5.2 Bootstrap Job failures

```bash
kubectl -n excalibase-platform logs job/platform-bootstrap
kubectl -n excalibase-platform get job platform-bootstrap -o jsonpath='{.status}'
```

* `initial admin registration failed` — provisioning refused
  `POST /api/auth/register`; check `kubectl logs deploy/provisioning` (platform-db).
* `vault already initialized but no stored key — inconsistent state` — the
  `platform-bootstrap` Secret was deleted after init; the Shamir share is
  gone. Restore the Secret from backup or wipe platform-db and reinstall.
* `activeDeadlineSeconds` hit (`bootstrap.timeoutSeconds`, 300 in
  `values-prod.yaml`; the nightly uses 600) — raise it and re-run
  `helm upgrade`; `before-hook-creation` keeps the old Job for inspection.
* `apk add` failing — no egress to the Alpine mirror (§1.1).

### 5.3 Vault sealed after a restart

Provisioning auto-unseals at boot from `VAULT_UNSEAL_KEY`, mounted from
`platform-bootstrap` (`optional: true`, `templates/provisioning.yaml`;
`server-go/pkg/vault/vault.go`). If the Secret or the key is missing the
pod is up but every credential lookup fails.

```bash
curl -s https://<admin host>/api/vault/status | jq       # {"initialized":true,"sealed":true}
# Option A: re-run the hook (re-unseals, idempotent)
helm upgrade platform ./charts/platform-aio -n excalibase-platform -f my-values-prod.yaml
# Option B: manual
UNSEAL=$(kubectl -n excalibase-platform get secret platform-bootstrap -o jsonpath='{.data.unseal-key}' | base64 -d)
curl -X POST -H "Authorization: Bearer $PAT" -H 'Content-Type: application/json' \
  -d "{\"key\":\"$UNSEAL\"}" https://<admin host>/api/vault/unseal
```

`GET /api/vault/status` is unauthenticated; `init` and `unseal` require a
PAT (`server-go/internal/handler/vault.go`). If option A leaves the vault
sealed, the token in the Secret has expired (§3.4 step 3): use option B
with a fresh admin PAT, then replace the Secret value. Anyone who can read
`platform-bootstrap` can unseal the vault — restrict RBAC on that Secret
(NOTES.txt). KMS envelope unseal (`vault.kmsUnseal.*`,
`server-go/pkg/kmsseal`) removes the plaintext key but needs AWS KMS; deferred.

### 5.4 `helm install --wait` deadlock on a first install

`templates/auth.yaml` and `templates/graphql.yaml` reference
`platform-bootstrap` with non-optional `secretKeyRef`s, and that Secret is
written by the **post-install hook** Job. With `--wait`, Helm waits for every
Deployment to become Ready before it runs post-install hooks; auth and
graphql cannot become Ready until the hook has run. Result: the install sits
in `CreateContainerConfigError` until `--timeout` expires and the release is
marked failed, even though nothing is wrong.

Do not pass `--wait` on the first install (the nightly `aio-e2e.yml` does
not). `scripts/install-all.sh` does pass `--wait --timeout 10m` on step C
and will hit this on a fresh cluster; it is safe on re-runs once the Secret
exists. No ticket found.

### 5.5 CNPG PDB makes a node undrainable

CNPG creates a `<cluster>-primary` PodDisruptionBudget with `minAvailable: 1`
whenever `spec.enablePDB` is true. On a single-instance cluster there is no
replica to fail over to, so `ALLOWED DISRUPTIONS` is 0 and `kubectl drain`
blocks forever (`values.yaml` comment on `platformDb.enablePDB`).

* platform-db: the chart sets `enablePDB: false` automatically when
  `platformDb.instances == 1` (`_helpers.tpl`).
* Tenant clusters: the CRD builder (`server-go/internal/k8s/crd_builder.go`)
  does not set `enablePDB`, so every single-instance tenant cluster gets the
  operator default. Before draining a node, list `kubectl get pdb -A` and
  either hibernate the affected projects (`POST /pause`) or patch
  `spec.enablePDB=false` on the Cluster. No ticket found for setting it at
  provision time.

## 6. Known gaps (verified, not yet shipped)

| Gap | Ticket |
|---|---|
| Release image tags / pinning (`v1.0.0` tags referenced by `values-prod.yaml` do not exist; watcher image hard-coded `:latest`) | EXC-47, EXC-332, EXC-346 |
| HPA for the AIO services | EXC-50 |
| Pause takes ~3 min because the watcher holds the replication session open | EXC-363 |
| CDC lag metric (alerts use retained-WAL as proxy) | EXC-34 |
| platform-db backup not wired (`platformDb.backup.*` values inert) | none found |
| `resources:` and `ingress.annotations:` in `values-prod.yaml` not rendered | none found |
| `EXCALIBASE_AUTOPAUSE_ENABLED`, `EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS`, `BYOC_EGRESS_ALLOWLIST` not chart values | none found |
| Bootstrap Job stores a 12h session token as `provisioning-pat`; auth/graphql break after 12h unless replaced (§3.4) | none found; incident recorded in `aio-e2e/k8s-dataplane/README.md` |
| HAProxy-as-Ingress: nginx-only annotations and regex paths | none found |
| PgDog: notifier wiring is present but PgDog itself is not deployed | tracked outside this repo |
