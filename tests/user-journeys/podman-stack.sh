#!/bin/bash
# Inside the gate's privileged Podman container (run.sh podman): rootful Podman, the single-host
# bundle up with `podman compose`, and a registry at localhost:5000 the platform pulls from.
# /src is the checkout (read-only), /gate the run's working directory.
set -euo pipefail
dnf install -y -q python3 openssl curl jq procps-ng gawk >/dev/null 2>&1
mkdir -p /usr/libexec/docker/cli-plugins
cp /host-compose/docker-compose /usr/libexec/docker/cli-plugins/docker-compose
printf '[containers]\nlog_driver = "k8s-file"\n[engine]\ncgroup_manager = "cgroupfs"\nevents_logger = "file"\nruntime = "crun"\n' \
  > /etc/containers/containers.conf
# The run's registry speaks plain HTTP on loopback, as `docker run registry` does for Docker.
printf '[[registry]]\nlocation = "localhost:5000"\ninsecure = true\n' > /etc/containers/registries.conf.d/gate.conf
mkdir -p /run/podman
# Detached from this exec session, with its own log in the run's artifacts.
setsid nohup podman --log-level=info system service --time=0 unix:///run/podman/podman.sock \
  > /gate/podman-service.log 2>&1 < /dev/null &
for _ in $(seq 30); do [ -S /run/podman/podman.sock ] && break; sleep 1; done
# No systemd here, so Podman schedules no healthcheck timers: run them.
setsid nohup bash -c 'while true; do for id in $(podman ps -q); do podman healthcheck run "$id" >/dev/null 2>&1 || true; done; sleep 5; done' \
  > /dev/null 2>&1 < /dev/null &
if [ -f /gate/images.tar ]; then
  podman load -q -i /gate/images.tar >/dev/null
  # docker save writes short names, which Podman loads under localhost/.
  for image in $(podman images --format '{{.Repository}}:{{.Tag}}' | grep '^localhost/'); do
    name=${image#localhost/}
    case ${name%%:*} in */*) ;; *) name=library/$name ;; esac
    podman tag "$image" "docker.io/$name"
  done
fi
podman run -d --name excalibase-gate-registry -p 5000:5000 \
  docker.io/library/registry@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373 >/dev/null

cp -a /src /work
cd /work/deploy/single-host
CGROUP_CONTROLLERS="cpu memory" UNPRIVILEGED_PORT_START=0 ./init.sh --env-file /gate/.env --domain localhost \
  --admin-email gate@example.com --engine podman --engine-socket /run/podman/podman.sock \
  --apps --app-sandbox "${APP_SANDBOX:-none}" >/dev/null
cat >> /gate/.env <<ENV
TAG=${TAG:-main}
PLATFORM_TAG=${PLATFORM_TAG:-${TAG:-main}}
FEATURE_MCP=true
APP_LOCAL_REGISTRIES=localhost:5000
ENV
export DOCKER_HOST=unix:///run/podman/podman.sock
podman compose -p excalibase-gate --env-file /gate/.env -f compose.yaml -f tests/boot-smoke.override.yml up -d
