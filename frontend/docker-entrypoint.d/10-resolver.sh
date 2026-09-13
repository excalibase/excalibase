#!/bin/sh
# Inject the runtime DNS resolver into the nginx config before nginx starts
# (the nginx image runs /docker-entrypoint.d/*.sh first). This makes the /api
# upstream resolve under both Docker (embedded DNS 127.0.0.11) and Kubernetes
# (cluster DNS from /etc/resolv.conf), rather than hard-coding Docker's IP.
set -e
RESOLVER=$(awk '/^nameserver/ {print $2; exit}' /etc/resolv.conf 2>/dev/null || true)
sed -i "s|__DNS_RESOLVER__|${RESOLVER:-127.0.0.11}|g" /etc/nginx/conf.d/default.conf
