# Single host: rootless Podman

The same bundle as [docker-local.md](./docker-local.md)
([`deploy/single-host/`](../../deploy/single-host/)), run by an unprivileged
user with rootless Podman (ADR 0026). Read docker-local.md first for what runs,
what is published and what works; this page covers what differs.

Verified from fresh state on Ubuntu 24.04 (Podman 4.9.3, AppArmor) and Fedora 44
(Podman 5.8.1, SELinux enforcing): install, sign-in, a Postgres project at its
tier's limits, GraphQL and REST with a secret key, a backup.

Nothing runs as root at run time: the engine is the user's own Podman, and the
engine proxy reaches the user's `podman.sock` (as the container's root, which in
a rootless container is that unprivileged user).

## Prerequisites

- Podman 4.9 or newer and `podman compose` backed by **Docker Compose v2**
  (`docker-compose`), which is what `podman compose` runs first when it is
  installed: Fedora/RHEL `dnf install podman docker-compose`, Debian/Ubuntu
  `apt install podman docker-compose-v2`. Check with `podman compose version`
  (it names the provider). Use the distribution's Podman, not a static build:
  healthchecks run through systemd user timers.

  `podman-compose` (1.6) is **not supported**: it turns every `depends_on`
  into a hard Podman dependency, so a service behind a finished one-shot
  (`preflight`, `bootstrap`) never starts, and it treats
  `service_completed_successfully` as "stopped", ignoring the exit code, so a
  failed secret check would not hold Postgres back.
- The user's Podman API socket, and lingering so the stack outlives your login:

  ```bash
  systemctl --user enable --now podman.socket
  sudo loginctl enable-linger "$USER"
  ```

- The `cpu` and `memory` cgroup controllers delegated to the user, so project
  databases run with their tier's limits. Fedora and Ubuntu 24.04 delegate
  them by default; Ubuntu 22.04 delegates only memory. `init.sh` checks and
  prints the fix, which is, as root:

  ```bash
  mkdir -p /etc/systemd/system/user@.service.d
  printf '[Service]\nDelegate=cpu cpuset io memory pids\n' > /etc/systemd/system/user@.service.d/delegate.conf
  systemctl daemon-reload   # then log in again
  ```

- Ports 80 and 443 for a public domain (Let's Encrypt): rootless processes
  cannot open ports below `net.ipv4.ip_unprivileged_port_start` (1024 by
  default). As root, once:

  ```bash
  echo 'net.ipv4.ip_unprivileged_port_start=80' > /etc/sysctl.d/90-excalibase.conf && sysctl --system
  ```

  For `localhost` `init.sh` moves the edge to 8080/8443 instead.

## Install

```bash
git clone https://github.com/excalibase/excalibase.git
cd excalibase/deploy/single-host
./init.sh --engine podman --domain example.com --admin-email you@example.com
podman compose version             # must name Docker Compose v2
podman compose up -d
podman compose logs bootstrap      # the first admin's generated password
```

`--engine podman` points the proxy at `$XDG_RUNTIME_DIR/podman/podman.sock`. On
an SELinux host (`getenforce` = Enforcing) it also writes
`ENGINE_PROXY_SELINUX_LABEL=disable`: a confined container may not connect to
the engine socket, so the proxy alone runs unconfined by SELinux (it still has
no capabilities and a read-only root). The bundle's file mounts carry `:z` so
the other containers can read them under SELinux.

## Differences from Docker

| | Docker | rootless Podman |
|---|---|---|
| Engine socket | `/var/run/docker.sock` (root) | the user's `podman.sock` |
| Proxy runs as | uid 1000, the socket's group | container root = the unprivileged user |
| Edge ports | 80/443 | 80/443 with the sysctl above, else 8080/8443 for `localhost` |
| Client addresses seen by the edge | the real client | the address of Podman's port forwarder: `STUDIO_ALLOW_CIDRS` and per-client rate limits cannot tell clients apart — restrict Studio with the host firewall instead |
| Tenant CPU limits | always | needs the `cpu` controller delegated |
| Image healthchecks | kept | dropped for OCI-format images (Podman); the bundle declares every health gate it relies on in `compose.yaml` |

## Apps (containers)

The same as on Docker (see [Apps in docker-local.md](docker-local.md#apps-containers)),
with one difference that matters: **rootless Podman cannot run apps under
gVisor with their plan's limits.** `runsc` started by rootless Podman cannot
create cgroups (`systemd error: Interactive authentication required`, or
`cgroup.subtree_control: permission denied` with cgroupfs); it runs only with
`--ignore-cgroups`, and then an app has no memory or CPU limit at all. So on
rootless Podman install apps without the sandbox, knowingly:

```bash
./init.sh --engine podman --domain example.com --admin-email you@example.com --apps --app-sandbox none
podman compose up -d
```

Apps then run with every capability dropped but `NET_BIND_SERVICE`,
`no-new-privileges`, the plan's memory, CPU and 1024 processes, under crun:
they share the host's kernel. Run only code you trust, or use Docker with
gVisor (or Kubernetes) for code you do not. Do not configure `runsc` with
`--ignore-cgroups` for apps: provisioning cannot tell, and the limits would be gone.

- With `APP_EGRESS=internet` each project network is created with netavark's
  `isolate` option, so a project's bridge cannot reach another's; without
  egress the networks are internal and have no route anywhere.
- App hostnames carry the edge's port when it is above 1024
  (`https://<app>-<project>.apps.localhost:8443`).
- The image's root directory may be read-only to an app without capabilities
  (Podman keeps the image's `/` mode): write to `/tmp` or the app's disk.
- Logs: Podman's `journald` or `k8s-file` driver; set `log_size_max` in
  `containers.conf` to bound `k8s-file`.

Running Docker and Podman stacks side by side on one host needs different
`AIO_EDGE_SUBNET` / `AIO_DATAPLANE_SUBNET` values: both claim the same subnets.

## DocumentDB projects

The same as on Docker (see [DocumentDB projects in docker-local.md](docker-local.md#documentdb-projects)):

```bash
./init.sh --engine podman --domain example.com --admin-email you@example.com --documentdb
podman compose up -d
# the CA from Studio's Connect card saved as ca.crt
podman run --rm -it --network host -v "$PWD/ca.crt:/ca.crt:ro,z" \
  docker.io/library/mongo@sha256:d0d926f94df099bff534b7ee5b5986458131a22489dfff8664509af0c1e2ca9c \
  mongosh --host 127.0.0.1 --port <mongo port> --tls --tlsCAFile /ca.crt \
  --authenticationMechanism SCRAM-SHA-256 -u postgres -p '<password>'
```

- Podman refuses to remove a database container while its gateway exists
  (the gateway depends on its network namespace). Provisioning removes the
  gateway first; by hand, remove `excalibase-<project>-documentdb` before
  `excalibase-<project>-postgres`:
  `podman rm -f $(podman ps -aq --filter label=excalibase.managed=true --filter name=-documentdb)`.
- The Mongo port is forwarded by Podman's rootless port forwarder on the host's
  `127.0.0.1`; `--network host` above is the user's own network namespace,
  which reaches it.
