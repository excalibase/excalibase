# Matrix 3: Docker local

The platform runs as a Docker container on a single host and provisions
Postgres databases on that same host via the Docker daemon socket.

Use this when:

- You don't want Kubernetes at all (note: a single-node K8s distro like k0s/k3s/RKE2/MicroK8s is also a valid single-host option — see matrix 1).
- You're willing to trade per-project isolation for operational simplicity.
- The host is single-tenant (you control everything that runs on it).

> **Trade-off vs K8s mode:** Docker mode is operationally simpler — no
> operator, no CRDs, no etcd — but the provisioner needs **root-equivalent
> access to the Docker daemon** (Podman has the same constraint unless
> explicitly running rootless), and there is no platform-enforced isolation
> between projects (no namespaces, no NetworkPolicies). For a single-host
> install with stronger isolation, consider a single-node K8s distro (k0s,
> k3s, RKE2, MicroK8s — including rootless) and use matrix 1 instead.

## Prerequisites

- Docker 24+ with the Docker Compose plugin
- `/var/run/docker.sock` accessible (default on Linux)

## docker-compose.yml

```yaml
version: "3.9"

services:
  provisioning:
    image: excalibase/provisioning:0.1.0
    restart: unless-stopped
    ports:
      - "24005:24005"
    environment:
      DEPLOYMENT_MODE: selfhosted
      PORT: "24005"
      CORS_ORIGINS: "http://localhost:5173,https://studio.example.com"
      PUBLIC_BASE_URL: "http://localhost:24005"
      STORAGE_PATH: /var/lib/excalibase
      DENO_RUNTIME_SECRET: ${DENO_RUNTIME_SECRET}

      # Docker provisioner
      PROVISIONER_MODE: docker
      # Falls back to /var/run/docker.sock if DOCKER_HOST is not set
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - excalibase-data:/var/lib/excalibase

  studio:
    image: excalibase/studio:0.1.0
    restart: unless-stopped
    ports:
      - "5173:80"
    environment:
      VITE_API_URL: "http://localhost:24005"
      VITE_DEPLOYMENT_MODE: selfhosted
    depends_on:
      - provisioning

volumes:
  excalibase-data:
```

Generate the runtime secret once and store it in `.env` next to the compose
file:

```bash
echo "DENO_RUNTIME_SECRET=$(openssl rand -hex 32)" > .env
chmod 600 .env
```

Then:

```bash
docker compose up -d
docker compose logs -f provisioning
```

## How the platform finds Docker

From `internal/provisioner/docker.go` (priority order matches the K8s client pattern):

1. **`DOCKER_HOST=tcp://...`** → remote TCP (matrix 4)
2. **`DOCKER_HOST=unix:///...`** → explicit unix socket path
3. **Default: `/var/run/docker.sock`** ← this matrix

Mount the socket read-write into the container. Docker Engine API calls go
over the socket, no network needed.

## Security note

Mounting `docker.sock` into a container gives that container root-equivalent
access to the host. The provisioner uses this access to create/destroy
project containers — there is no Kubernetes-style ServiceAccount + Role
constraining what it can do; if compromised it can run arbitrary containers,
read every secret, and reach every other container on the daemon. Only do
this on hosts you trust and where the platform is the only tenant.

For multi-tenant scenarios, use matrix 4 (remote Docker + TLS) against a
dedicated Docker host, **or** switch to matrix 1/2 (K8s) where the platform
authenticates with a least-privilege ServiceAccount and projects are
namespace-isolated.

## Verification

```bash
# Health
curl http://localhost:24005/healthz

# Create a Postgres project on the local Docker host
curl -X POST http://localhost:24005/api/provision \
  -H "Content-Type: application/json" \
  -d '{"projectName":"local-pg","orgId":"default","databaseType":"POSTGRESQL","tier":"FREE"}'

# The provisioner creates a Docker container — verify
docker ps | grep default-local-pg
```

## Troubleshooting

- **`permission denied` on docker.sock** — the user inside the container
  doesn't have access. Either run as root (default for the provisioning
  image) or add the container user to the `docker` group on the host.
- **`Cannot connect to the Docker daemon`** — socket not mounted, or the
  container path differs. Check the `volumes:` entry.
- **Containers created but unreachable** — the platform creates containers on
  the default bridge network by default. To reach them from the host, publish
  the Postgres port or join them to the compose network.
- **Data loss after restart** — make sure `excalibase-data` volume is named
  and persistent. An anonymous volume gets garbage-collected.
