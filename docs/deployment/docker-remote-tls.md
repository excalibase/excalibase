# Matrix 4: Docker remote + TLS

The platform runs somewhere (binary, container, another host) and provisions
Postgres containers on a **remote Docker host** over TCP with mutual TLS.

Use this when:

- You want Docker mode but don't want to mount the local socket.
- Your Docker host is on a dedicated VM you manage separately.
- You need multiple platform instances provisioning to the same Docker host.

> **Status:** requires `PROVISIONER_MODE=docker`, which is being implemented.
> Until it lands, the K8s provisioner is the default.

## Target Docker host setup

On the Docker host, enable the remote TLS socket per the Docker docs
(<https://docs.docker.com/engine/security/protect-access/>). Summary:

1. Generate CA, server, and client certs.
2. Start `dockerd` with:

   ```bash
   dockerd \
     --tlsverify \
     --tlscacert=/etc/docker/certs/ca.pem \
     --tlscert=/etc/docker/certs/server-cert.pem \
     --tlskey=/etc/docker/certs/server-key.pem \
     -H=0.0.0.0:2376 \
     -H=unix:///var/run/docker.sock
   ```

3. Open TCP 2376 only to the platform host (firewall or security group).

## Platform environment

Copy the **client** cert bundle (`ca.pem`, `cert.pem`, `key.pem`) to a
directory on the platform host. Then:

```bash
export DEPLOYMENT_MODE=selfhosted
export CORS_ORIGINS="https://studio.example.com"
export PUBLIC_BASE_URL="https://api.example.com"
export STORAGE_PATH=/var/lib/excalibase
export DENO_RUNTIME_SECRET=$(openssl rand -hex 32)

export PROVISIONER_MODE=docker
export DOCKER_HOST="tcp://dockerhost.example.com:2376"
export DOCKER_TLS_VERIFY=1
export DOCKER_CERT_PATH=/etc/excalibase/docker-certs

./excalibase-server
```

The contents of `DOCKER_CERT_PATH` must be:

```
/etc/excalibase/docker-certs/
├── ca.pem
├── cert.pem
└── key.pem
```

## How the platform finds the host

From the Docker client priority order (matches the K8s client pattern):

1. **`DOCKER_HOST=tcp://...` + `DOCKER_TLS_VERIFY=1` + `DOCKER_CERT_PATH=...`**
   ← this matrix
2. `DOCKER_HOST=unix:///...` — explicit socket path
3. Fallback: `/var/run/docker.sock` (matrix 3)

Internally the client uses `github.com/docker/docker/client.NewClientWithOpts`
with `FromEnv`, which reads these standard variables — the same ones the
`docker` CLI uses. You can sanity-check your setup by running:

```bash
DOCKER_HOST=tcp://... DOCKER_TLS_VERIFY=1 DOCKER_CERT_PATH=... docker ps
```

If that works, the provisioning binary will connect the same way.

## docker-compose.yml (platform as container)

If the platform itself runs as a container on a third host:

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
      CORS_ORIGINS: "https://studio.example.com"
      PUBLIC_BASE_URL: "https://api.example.com"
      STORAGE_PATH: /var/lib/excalibase
      DENO_RUNTIME_SECRET: ${DENO_RUNTIME_SECRET}
      PROVISIONER_MODE: docker
      DOCKER_HOST: "tcp://dockerhost.example.com:2376"
      DOCKER_TLS_VERIFY: "1"
      DOCKER_CERT_PATH: /certs
    volumes:
      - ./docker-certs:/certs:ro
      - excalibase-data:/var/lib/excalibase

volumes:
  excalibase-data:
```

## Verification

```bash
./excalibase-server &
curl http://localhost:24005/health

# Create a project — it should land on the remote Docker host
curl -X POST http://localhost:24005/api/provision \
  -H "Content-Type: application/json" \
  -d '{"projectName":"remote-pg","orgId":"default","databaseType":"POSTGRESQL","tier":"FREE"}'

# Verify the container on the remote host
ssh dockerhost docker ps | grep default-remote-pg
```

## Troubleshooting

- **`x509: certificate signed by unknown authority`** — `DOCKER_CERT_PATH`
  missing `ca.pem` or mismatched CA. The CA that signed the *server* cert
  must match `ca.pem` in the client directory.
- **`tls: bad certificate`** — server cert's SAN list doesn't include the
  hostname you set in `DOCKER_HOST`. Regenerate with the right DNS name.
- **Connection refused on 2376** — `dockerd` not listening on TCP, or the
  firewall dropped the packet.
- **Slow cold starts** — TLS handshake is ~30-80ms. Normal. Pool connections
  if it's a problem (the Docker SDK does by default).
