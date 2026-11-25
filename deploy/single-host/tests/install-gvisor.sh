#!/usr/bin/env sh
# Installs gVisor (runsc) into the runner's Docker and waits until the daemon serves it (EXC-801).
# `systemctl reload docker` returns before dockerd has re-read daemon.json, so the next `docker`
# call raced it and failed right after the install.
set -eu

curl -fsSL https://gvisor.dev/archive.key | sudo gpg --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main" | sudo tee /etc/apt/sources.list.d/gvisor.list >/dev/null
sudo apt-get update -qq && sudo apt-get install -y -qq runsc
# Apps reach their database by name through Docker's resolver, which gVisor's own network stack cannot reach.
sudo runsc install -- --network=host
sudo systemctl reload docker

for i in $(seq 60); do
  if docker info --format '{{json .Runtimes}}' 2>/dev/null | grep -q runsc; then
    echo "dockerd serves runsc after ${i} check(s)"
    exit 0
  fi
  sleep 1
done
echo "dockerd did not report the runsc runtime within 60s" >&2
docker info --format '{{json .Runtimes}}' >&2 || true
exit 1
