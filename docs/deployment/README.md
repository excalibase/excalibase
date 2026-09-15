# Deployment

Excalibase Provisioning ships as a single Go binary + React frontend. Pick a
deployment matrix based on where the platform runs and where it provisions
databases.

| # | Matrix | Platform runs on | Provisions to | Page |
|---|---|---|---|---|
| 1 | **K8s in-cluster** | Kubernetes | Same cluster (ServiceAccount) | [k8s-in-cluster.md](./k8s-in-cluster.md) |
| 2 | **K8s remote** | Binary / Docker / another cluster | Remote Kubernetes via API + bearer token | [k8s-remote.md](./k8s-remote.md) |
| 3 | **Docker local** | Docker host | Same host via `/var/run/docker.sock` | [docker-local.md](./docker-local.md) |
| 4 | **Docker remote + TLS** | Anywhere | Remote Docker host over TCP + TLS | [docker-remote-tls.md](./docker-remote-tls.md) |

All four support both deployment modes:

- **`DEPLOYMENT_MODE=selfhosted`** (default) — Postgres platform store and
  vault, single default org, no billing/tier enforcement. All features
  unlocked. On docker the vault auto-inits on first boot and auto-unseals on restart; on k8s the chart bootstrap Job does this.
- **`DEPLOYMENT_MODE=cloud`** — Postgres platform store and vault, multi-org,
  tier enforcement. Vault init/unseal is done by the bootstrap Job.

The platform store and the vault are **Postgres in both modes** —
`PLATFORM_DB_URL` is always required (SQLite and bbolt were removed to avoid
maintaining two backends). On a single host, run a Postgres container
alongside the platform for its own state (see docker-local.md).

## Which matrix should I use?

- **Running on Kubernetes and want to provision databases on the same
  cluster?** → matrix 1.
- **Running the platform outside Kubernetes (dev laptop, VM, different
  cluster) but want workloads provisioned on a target Kubernetes cluster?** →
  matrix 2.
- **Don't want Kubernetes, single host you control?** → matrix 3.
- **Don't want Kubernetes, dedicated remote Docker host?** → matrix 4.

> **Single-host ≠ Docker-only.** Single-node K8s distros (k0s, k3s, RKE2,
> MicroK8s — including rootless) are first-class production targets for
> matrix 1. Docker mode trades operational simplicity for a weaker isolation
> surface — the daemon is root-equivalent and there are no per-project
> namespaces or NetworkPolicies. Pick K8s mode when you want that surface;
> pick Docker mode when you want fewer moving parts.

Matrices 3 and 4 use the Docker provisioner (`PROVISIONER_MODE=docker`).
K8s provisioner is the default.

## Shared environment variables

These apply to every matrix. Matrix-specific pages cover the rest.

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `DEPLOYMENT_MODE` | no | `selfhosted` | `selfhosted` or `cloud` |
| `PORT` | no | `24005` | HTTP listen port |
| `CORS_ORIGINS` | yes | `https://app.excalibase.io` | Comma-separated allow-list. No default means no origin allowed — fail-closed. |
| `PUBLIC_BASE_URL` | yes | `https://api.excalibase.io` | Base URL for edge function invoke + SDK snippets. |
| `LOG_LEVEL` | no | `debug` | `debug`, `info`, `warn`, `error` |
| `STORAGE_PATH` | no | `../provisioning-data` | On-disk dir for the auto-generated vault unseal key (`unseal.key`). Docker provisioner only; unused when `VAULT_UNSEAL_KEY` is set or on k8s (the bootstrap Job holds the key). |
| `PLATFORM_DB_URL` | **yes** | — | Postgres DSN for the platform store. Required in both modes (no SQLite fallback). |
| `VAULT_URL` | optional | — | If set, use remote vault over HTTP instead of embedded. |
| `VAULT_PAT` | with `VAULT_URL` | — | Personal access token for remote vault. |
| `NATS_URL` | optional | — | NATS server for PgDog reload signals. |
| `DENO_RUNTIME_IMAGE` | no | `excalibase/deno-runtime:latest` | Image used for per-project edge function runtimes. |
| `DENO_NAMESPACE` | no | `serverless` | Namespace for the shared-fallback Deno runtime. |
| `DENO_RUNTIME_SECRET` | yes (for edge functions) | — | HMAC secret between platform and Deno runtime. |
| `EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS` | no | — | Operator floor for edge-function outbound calls (`host`, `host:port`, `*.suffix`, comma-separated), unioned into every project's own allowlist. Empty = no egress until a project sets `PUT /api/projects/{id}/functions/egress`. See [functions-egress.md](../functions-egress.md). |
