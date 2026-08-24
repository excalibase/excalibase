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
# One shared, explicitly-named network. Its name MUST equal DOCKER_NETWORK below:
# provisioning attaches each tenant DB container to that network, and graphql /
# provisioning reach those DBs by container name — the default bridge has no name
# resolution.
networks:
  excalibase:
    name: excalibase

services:
  # Caddy terminates TLS (automatic Let's Encrypt) and fronts the HTTP
  # services. Tenant Postgres containers are TCP and do NOT go through Caddy.
  caddy:
    image: caddy:2
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy-data:/data
      - caddy-config:/config
    networks: [excalibase]
    depends_on:
      - provisioning
      - studio

  provisioning:
    image: excalibase/provisioning:0.1.0
    restart: unless-stopped
    # No host port publish — Caddy reaches it over the compose network.
    expose:
      - "24005"
    environment:
      DEPLOYMENT_MODE: selfhosted
      PORT: "24005"
      # Postgres is required in BOTH modes — the platform store has no SQLite
      # fallback. This points at the platform-db service below.
      PLATFORM_DB_URL: "postgres://platform:${PLATFORM_DB_PASSWORD}@platform-db:5432/platform?sslmode=disable"
      CORS_ORIGINS: "https://studio.example.com"
      PUBLIC_BASE_URL: "https://api.example.com"
      STORAGE_PATH: /var/lib/excalibase           # bbolt vault only
      DENO_RUNTIME_SECRET: ${DENO_RUNTIME_SECRET}
      # Docker provisioner (single-tenant; docker + cloud is refused at boot).
      PROVISIONER_MODE: docker
      # DB container ports bind 127.0.0.1 by default (internal only). Set
      # true only to expose provisioned DBs on the host interface for
      # external clients.
      DOCKER_DB_PUBLIC: "false"
      # Attach provisioned tenant DBs to the shared network (must match the
      # network name above) so graphql/schema can resolve them by name.
      DOCKER_NETWORK: excalibase
      # Plain postgres containers have no TLS; intra-network connects use disable.
      SCHEMA_DB_SSLMODE: disable
    # The container user (uid 1000) needs the host's docker group to use the
    # socket. Replace 999 with your host's gid: `stat -c %g /var/run/docker.sock`.
    group_add:
      - "999"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - excalibase-data:/var/lib/excalibase
    networks: [excalibase]
    depends_on:
      platform-db:
        condition: service_healthy

  # The platform's OWN state (users, orgs, projects, tokens). Not a tenant DB.
  platform-db:
    image: postgres:17
    restart: unless-stopped
    environment:
      POSTGRES_USER: platform
      POSTGRES_PASSWORD: ${PLATFORM_DB_PASSWORD}
      POSTGRES_DB: platform
    # provisioning's Postgres connect does not retry — gate startup on readiness.
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U platform -d platform"]
      interval: 3s
      timeout: 3s
      retries: 20
    volumes:
      - platform-db-data:/var/lib/postgresql/data
    networks: [excalibase]

  studio:
    image: excalibase/studio:0.1.0
    restart: unless-stopped
    expose:
      - "80"
    environment:
      VITE_API_URL: "https://api.example.com"
      VITE_DEPLOYMENT_MODE: selfhosted
    networks: [excalibase]
    depends_on:
      - provisioning

  # Data plane — serves the tenant's GraphQL + REST at /{projectId}/graphql and
  # /{projectId}/api/v1. It has NO static database: POSTGRES_URL is unset, so it
  # runs in provisioning-backed routing and fetches each project's DB creds from
  # provisioning at request time (the provisioned DBs don't exist until the
  # customer creates them). Caddy routes the project-scoped paths here.
  graphql:
    image: excalibase/excalibase-graphql:0.0.1
    restart: unless-stopped
    expose:
      - "10000"
    environment:
      # Multi-tenant/Mode-3 requires POSTGRES_URL to be EXPLICITLY EMPTY.
      # Omitting it falls back to graphql's dev default datasource and stays
      # single-datasource (no per-project schema). Empty → multi-tenant only.
      POSTGRES_URL: ""
      PROVISIONING_URL: "http://provisioning:24005/api"
      PROVISIONING_PAT: ${GRAPHQL_PROVISIONING_PAT}
      # REQUIRED for multi-tenant: graphql builds the per-tenant GraphQL schema
      # from the JWT's projectId+orgSlug. Without JWT it serves an empty schema.
      JWT_ENABLED: "true"
      # End-user JWT validation. Simplest single-tenant option: an HS256 shared
      # secret the tenant's own backend signs with. If you also run
      # excalibase-auth, set APP_SECURITY_AUTH_JWKS_URL to its JWKS instead.
      APP_SECURITY_AUTH_HMAC_SECRET: ${GRAPHQL_JWT_HMAC_SECRET}
    networks: [excalibase]
    depends_on:
      - provisioning

volumes:
  excalibase-data:
  platform-db-data:
  caddy-data:
  caddy-config:
```

### Caddyfile

Caddy auto-provisions Let's Encrypt certificates for these hostnames (point
their DNS at this host first). HTTP→HTTPS redirect and HSTS are automatic.

```caddy
api.example.com {
	# Data plane (tenant apps) → graphql. Project-scoped paths only.
	@dataplane path_regexp dp ^/[^/]+/(graphql|api/v1)
	handle @dataplane {
		reverse_proxy graphql:10000
	}
	# Everything else is the control plane on provisioning:
	#   /auth/*        end-user auth        (public)
	#   /functions/v1/* edge-function invoke (public)
	#   /api/*         admin/studio backend (gate this if internet-facing)
	handle {
		reverse_proxy provisioning:24005
	}

	# Internet-facing? The admin API (/api/*) should not be open — gate it to
	# your IPs while the data plane and function-invoke stay public:
	#   @admin path /api/*
	#   handle @admin {
	#     @allowed remote_ip 203.0.113.0/24 198.51.100.7
	#     handle @allowed { reverse_proxy provisioning:24005 }
	#     respond 403
	#   }
}

studio.example.com {
	reverse_proxy studio:80
}
```

### Data plane: the graphql PAT

graphql fetches each project's DB credentials from provisioning, which requires
a token with the credential-view permission. After first boot, mint one in the
studio as the platform admin (Settings → Tokens) or via the API, and put it in
`.env` as `GRAPHQL_PROVISIONING_PAT`. A plain end-user account's token will not
work — that call is deliberately privileged.

End-users of the tenant's app authenticate with a JWT (HS256 signed with
`GRAPHQL_JWT_HMAC_SECRET`, or via excalibase-auth's JWKS). Row-level security
keys off that JWT, so `JWT_ENABLED=true` is required for RLS to apply.

Generate the secrets once and store them in `.env` next to the compose file:

```bash
{
  echo "DENO_RUNTIME_SECRET=$(openssl rand -hex 32)"
  echo "PLATFORM_DB_PASSWORD=$(openssl rand -hex 24)"
  echo "GRAPHQL_JWT_HMAC_SECRET=$(openssl rand -hex 32)"
  # Filled in after first boot — see "the graphql PAT" above.
  echo "GRAPHQL_PROVISIONING_PAT="
} > .env
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
