#!/bin/bash
# wait-for-fence: the CNI programs a new pod's NetworkPolicy asynchronously, so
# no user code runs until egress is fenced. Canary: the kubernetes API Service,
# which always listens and is private. Refuse to build after ~60 s.
canary_host=${KUBERNETES_SERVICE_HOST:?}
canary_port=${KUBERNETES_SERVICE_PORT:-443}
for _ in $(seq 1 120); do
  if ! timeout 1 bash -c "exec 3<>/dev/tcp/${canary_host}/${canary_port}" 2>/dev/null; then
    echo "network fence up after ~${SECONDS}s"
    exit 0
  fi
  sleep 0.5
done
echo "the network fence never came up; refusing to build"
exit 1
