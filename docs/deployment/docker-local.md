# Single host: Docker

The platform on one machine with Docker, from the bundle in
[`deploy/single-host/`](../../deploy/single-host/). Rootless Podman runs the
same bundle; see [podman.md](./podman.md).

Use this when you want Excalibase on one VPS or server you control, without
Kubernetes. Kubernetes (the `platform-aio` chart, matrix 1) stays the primary
install: per-project namespaces, NetworkPolicies, gVisor and HA are Kubernetes
features. A single-node Kubernetes (k3s, RKE2, k0s) is the way to get those on
one machine.

## What runs

| Service | Published | Networks | Role |
|---|---|---|---|
| `edge` (Caddy) | **80, 443** | edge, dataplane | HTTPS for `studio.<domain>`, `api.<domain>` and `files.<domain>` |
| `studio` | no | edge | Studio SPA; proxies `/api` to provisioning |
| `provisioning` | no | platform, engine, tenants, edge | control plane |
| `engine-proxy` | no | engine | holds the Docker socket; forwards only what provisioning needs |
| `auth`, `graphql` | no | platform, dataplane, tenants | data plane: every project at `/{projectId}/graphql`, `/{projectId}/api/v1`, `/auth/{org}/{projectId}` |
| `postgres`, `nats` | no | platform | the platform's own state, change events |
| `objectstore` (RustFS) | no | platform, dataplane | project backups and customer files (EXC-578) |
| `bootstrap`, `preflight`, `objectstore-setup` | no | platform / none | one-shot: first admin + service tokens; secret check; buckets and keys |

Only the edge is published. `platform`, `engine` and `dataplane` are internal
networks with no route in or out. Tenant databases join `excalibase-tenants`
and publish their port on `127.0.0.1` only.

### The engine socket

Holding the Docker socket is root on the host, so provisioning does not get it.
`engine-proxy` holds it and answers provisioning's calls only:

- container create, start, stop, inspect, delete, file copy and `exec`, and
  only on containers labelled `excalibase.managed=true` — the platform's own
  containers cannot be stopped, exec'd into or removed through it;
- a create is refused if it asks for privileged mode, added capabilities,
  host paths (binds, bind mounts, volume driver options), devices, host
  namespaces (pid, ipc, uts, user, cgroup, network), unconfined security
  profiles, sysctls, another container's volumes, a network other than
  `excalibase-tenants`, a port on any address but `127.0.0.1`, or any
  HostConfig field the proxy does not know;
- image pulls are allowed; listing containers, networks, volumes, builds,
  swarm and everything else is refused.

A compromised provisioning can therefore start tenant-shaped containers but
cannot reach the host through the engine.

## Prerequisites

- Linux host with Docker Engine 24+ and the Compose plugin (`docker compose version`).
- A DNS name pointing at the host with three records: `studio.<domain>`,
  `api.<domain>` and `files.<domain>` (a wildcard `*.<domain>` works). Ports 80 and 443 reachable
  from the internet for Let's Encrypt. For a trial on your own machine use
  `localhost`: browsers resolve `*.localhost` to the machine, and the edge uses
  its own CA (your browser will warn until you trust it).
- Nothing for backups to start with: the bundled object store holds them.
  For disaster recovery an S3-compatible bucket off this machine (Cloudflare
  R2, Backblaze B2, AWS S3) is better; see [Object storage](#object-storage).
- 4 CPU / 8 GiB RAM is comfortable for the platform plus a few projects.

## Install

```bash
git clone https://github.com/excalibase/excalibase.git
cd excalibase/deploy/single-host
./init.sh --domain example.com --admin-email you@example.com
```

`init.sh` writes `.env` (mode 600): random 64-hex secrets
(`POSTGRES_PASSWORD`, `SETUP_TOKEN`, and the object store's
`OBJECTSTORE_ROOT_SECRET`, `OBJECTSTORE_BACKUPS_SECRET`,
`OBJECTSTORE_FILES_SECRET`), where browsers open file links
(`FILES_PUBLIC_URL`), the domain, how
certificates are obtained (`EXCALIBASE_TLS`: your address for Let's Encrypt, or
`internal` for `localhost`), who may open Studio (`STUDIO_ALLOW_CIDRS`, default
everyone — narrow it with `--studio-allow "203.0.113.0/24"`), and the engine
socket and its group. It refuses to overwrite an existing `.env`: the database
was created with those secrets.

`--manual-unseal` makes the vault start sealed after every restart and keeps
its key off the host; see [Manual vault unseal](#manual-vault-unseal).

Optional: email for invitations and password resets — `EMAIL_PROVIDER=resend`
with `RESEND_API_KEY`, or `EMAIL_PROVIDER=smtp` with your server's settings, and
`EMAIL_FROM_ADDRESS`. Without it email is off: invitations and resets cannot be
sent.

Start it:

```bash
docker compose up -d
docker compose logs bootstrap      # the first admin's generated password, printed once
```

Open `https://studio.<domain>` and sign in as `admin` with that password.
Create a project; its API is `https://api.<domain>/<projectId>/graphql` and
`https://api.<domain>/<projectId>/api/v1/<table>`. Each project's database is a
container named `excalibase-<projectId>-postgres` with the tier's memory and
CPU; its port is published on `127.0.0.1` only (reach it from elsewhere with an
SSH tunnel).

### Settings

| `.env` | Default | Meaning |
|---|---|---|
| `EDGE_BIND_ADDRESS` | `0.0.0.0` | Host address the edge listens on |
| `EDGE_HTTP_PORT` / `EDGE_HTTPS_PORT` | `80` / `443` | Host ports of the edge |
| `REGISTRATION_MODE` | `invite` | `open` lets anyone who reaches Studio sign up |
| `ADMIN_PASSWORD` | generated | First admin's password (12+ characters) |
| `TAG` | `latest` | Image tag of the platform services |
| `AIO_EDGE_SUBNET` / `AIO_DATAPLANE_SUBNET` | `10.203.250.0/28` / `10.203.250.16/28` | Change if they clash with your network |

## Object storage

The `objectstore` service ([RustFS](https://github.com/rustfs/rustfs),
Apache-2.0, pinned by digest) holds two buckets, each with its own key:
`excalibase-backups` (project backups) and `excalibase-storage` (customer
files, Studio Storage). `objectstore-setup` creates the buckets, one user per
bucket whose policy covers that bucket alone, and the file bucket's CORS; its
log ends with `OBJECT STORE READY`. The root key is used by that one-shot only.

- It is never published. The edge reaches only `excalibase-storage`, at
  `https://files.<domain>`, and every object there needs a link provisioning
  signed. Tenant containers cannot reach the store.
- Its data is in the `excalibase-platform-objectstore` volume, on the same
  disk as the databases. A lost disk loses the backups too: copy the volume
  off the host, or send backups to an outside bucket.

To use an outside bucket instead, set all of these in `.env` and run
`docker compose up -d` again:

```bash
BACKUP_DEFAULT_ACCESS_KEY_ID=...
BACKUP_DEFAULT_SECRET_ACCESS_KEY=...
BACKUP_DEFAULT_ENDPOINT=https://<account>.r2.cloudflarestorage.com
BACKUP_DEFAULT_BUCKET=excalibase-backups
BACKUP_DEFAULT_REGION=auto
# Customer files likewise: STORAGE_ACCESS_KEY_ID, STORAGE_SECRET_ACCESS_KEY,
# STORAGE_ENDPOINT (an address browsers reach), STORAGE_BUCKET, STORAGE_REGION,
# and STORAGE_INTERNAL_ENDPOINT= (empty: provisioning uses STORAGE_ENDPOINT too).
```

| `.env` | Default | Meaning |
|---|---|---|
| `FILES_PUBLIC_URL` | `https://files.<domain>` (init.sh) | Where browsers open file links; the edge serves it |
| `FILES_CORS_ORIGINS` | `*` | Origins allowed to use file links from a browser |

## Manual vault unseal

By default provisioning keeps the vault's unseal key in its volume and unseals
itself. With `./init.sh ... --manual-unseal` (or `VAULT_UNSEAL_PROVIDER=manual`
in `.env` before the first start) the key is never stored on the host:

1. After `docker compose up -d` the bootstrap log says the vault is
   uninitialized. Sign in to `https://studio.<domain>` as `admin`: Studio opens
   the setup screen. Initialize the vault and keep the key it shows once (a
   password manager, or shares split among people).
2. After every restart of provisioning (upgrade, reboot) the vault is sealed:
   Studio shows the unseal screen, `auth` waits and logs `the vault is sealed`,
   and project APIs answer errors until an admin unseals it, in Studio or with
   `docker exec -it excalibase-provisioning excalibase-provisioning vault unseal`
   (it asks for an admin token and the key without echo; `vault status` shows
   the state).

Lose every copy of the key and the vault, with the projects' credentials in
it, cannot be recovered. Unseal attempts are limited to 10 a minute per address
and recorded in the audit log, without the key.

## Apps (containers)

Containers (app hosting) run on the same host when the install asks for them
(ADR 0039). Studio and the API are the ones a Kubernetes install has: deploy an
image, open it at its hostname over HTTPS, read logs, redeploy and roll back,
pause, resume, delete, a disk, custom domains.

```bash
# gVisor (recommended): apps run under runsc, a user-space kernel.
# Debian/Ubuntu, from gVisor's apt repository (others: https://gvisor.dev/docs/user_guide/install/)
curl -fsSL https://gvisor.dev/archive.key | sudo gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" \
  | sudo tee /etc/apt/sources.list.d/gvisor.list
sudo apt-get update && sudo apt-get install -y runsc
# --network=host: apps find their database by name through Docker's resolver,
# which gVisor's own network stack cannot reach. Provisioning checks it at startup.
sudo runsc install -- --network=host && sudo systemctl restart docker
docker info | grep runsc              # must list it

./init.sh --domain example.com --admin-email you@example.com --apps
# point *.apps.example.com at this host (a wildcard DNS record), then
docker compose up -d
```

`--app-domain run.example.org` serves apps elsewhere than `apps.<domain>`.
Without gVisor provisioning refuses to start with apps on and says so;
`--app-sandbox none` (`APP_SANDBOX_RUNTIME=none`) runs apps without it, with
every capability dropped but `NET_BIND_SERVICE`: their code then shares the
host's kernel directly. Choose it only for code you trust.

Each app is a container named `excalibase-app-...`, labelled
`excalibase.managed=true`, at the plan's memory and CPU, no swap, at most 1024
processes, `no-new-privileges`, no published port and no host path. The edge
serves `https://<app>-<project>.<app domain>` with a certificate of its own for
each host (Let's Encrypt for a public domain, its own CA for `localhost`). Studio shows a host or custom domain as
issued once the edge serves a certificate for it that a browser would accept;
with the edge's own CA (`EXCALIBASE_TLS=internal`), as soon as it is routed.

| | Kubernetes | Single host |
|---|---|---|
| Sandbox | gVisor RuntimeClass | gVisor `runsc` with `--network=host` (gVisor's syscall sandbox; sockets use the host kernel's network stack inside the app's namespace), or the opt-out |
| Between projects | NetworkPolicy | one internal network per project (`excalibase-net-<project>`): only its apps, its database and the edge are on it |
| Between a project's apps | closed until the project turns its private network on | always open: they share the project's network, and the private-network switch is not offered |
| Internet from an app | allowed, private ranges denied | none; `APP_EGRESS=internet` in `.env` gives every project's network a route out (Podman: an isolated bridge) |
| Disk | a volume of the size asked | a named volume (`excalibase-vol-...`) on the host's disk; the size is recorded, not enforced by the filesystem |
| Readiness | kubelet probe | the platform's probe, copied into the container, on the app's health check path |
| Rollout | rolling update; with a disk, recreate | the same: new containers beside the old, the route moves when they are ready |
| Certificates | cert-manager | the edge, one per hostname |
| Copies | up to the plan's | one host: a plan with more copies is refused |

```bash
docker ps --filter label=excalibase.component=app    # running apps
docker network ls --filter label=excalibase.managed=true
docker volume ls --filter label=excalibase.managed=true
docker compose exec edge ls /etc/caddy/apps            # one route file per app
```

Deleting a project removes its apps' containers, disks, routes and network.
Apps' logs are the engine's (`docker logs`); rotate them with the daemon's
`log-opts` (`max-size`, `max-file`).

| `.env` | Default | Meaning |
|---|---|---|
| `APP_HOSTING_ENABLED` | `false` (`true` with `--apps`) | Containers on this host |
| `APP_DOMAIN` | `apps.<domain>` | Parent of every app hostname |
| `APP_SANDBOX_RUNTIME` | `runsc` | `none` runs apps without gVisor |
| `APP_EGRESS` | `none` | `internet` lets apps call out |
| `APP_DOMAIN_RESOLVER` | the host's | `host:port` of the DNS server custom domains' CNAMEs are checked against |

## Refusals you may see

- `run ./init.sh first` — a required value is missing from `.env`.
- `app hosting: the engine has no "runsc" runtime` — apps are on and gVisor is
  not installed: install it, or set `APP_SANDBOX_RUNTIME=none` knowingly.
- `a container under runtime "runsc" cannot resolve container names` — gVisor
  runs with its own network stack: reinstall it with `sudo runsc install -- --network=host`
  and restart Docker. Its sockets then use the host kernel's network stack,
  still inside the app's own network namespace.
- `preflight: ... is a known default` / `must be at least 32 hex characters` /
  `must all differ` — the stack does not start on a weak or reused secret.
  `docker compose logs preflight` names it.

## What works on a single host today

| | |
|---|---|
| Studio, sign-in, invitations (with email), organisations | yes |
| Postgres projects (create, pause, resume, delete, backups to the bundled store or your bucket) | yes, at the tier's memory and CPU |
| Storage (customer files, signed links at `files.<domain>`) | yes |
| GraphQL / REST / end-user auth for every project, through `api.<domain>` | yes |
| SDK keys (publishable, secret) | yes |
| Containers (app hosting) | yes, with `--apps` (see Apps) |
| DocumentDB projects | not yet |
| Edge functions, realtime | no (Kubernetes only) |
| High availability (3/5 copies) | no — one machine has one disk and one kernel (ADR 0038); a tier with more than one copy is refused. Use Kubernetes across nodes |

## Checking a change

`deploy/single-host/tests/boot-smoke.sh docker` boots the bundle from fresh
state and walks the first hour (weak secret refused, install, sign-in, project,
GraphQL/REST with a secret key, backup, a file uploaded and downloaded through
`files.<domain>`). `APPS=1` installs with `--apps` and walks Containers: a
busybox image by digest deployed, opened at its host, its logs read, a second
revision and a rollback, data kept on its disk, another project unable to reach
the app or its database, pause, resume and delete (under gVisor where the
engine has `runsc`, else with `--app-sandbox none`, which the run prints).
`MANUAL_UNSEAL=1` installs with `--manual-unseal` and also
initializes, restarts and unseals the vault with the CLI. It destroys the bundle's containers and
volumes: never run it on an install you keep. CI runs it on every change to the
bundle or the engine path; for Podman, `COMPOSE="podman compose" DOCKER=podman
.../boot-smoke.sh podman`.

## Operations

```bash
docker compose ps                    # health
docker compose logs -f provisioning
docker compose down                  # stop; volumes and tenant containers stay
docker compose down -v               # also delete the platform's volumes
docker ps -a --filter label=excalibase.managed=true   # tenant containers
```

`down -v` does not remove tenant containers, apps' disks or project networks
(provisioning creates them, not Compose). Remove them with

```bash
docker rm -f -v $(docker ps -aq --filter label=excalibase.managed=true)
docker volume rm $(docker volume ls -q --filter label=excalibase.managed=true)
docker network rm $(docker network ls -q --filter label=excalibase.managed=true)
```
Keep `.env` with the volumes: a fresh `.env` cannot open an existing database.
