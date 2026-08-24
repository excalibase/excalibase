#!/usr/bin/env bash
# Healthcheck: WAL-G binary responds AND we've ticked the heartbeat
# in the last minute. Either failure flips the container unhealthy
# so docker / kubelet restarts us.
set -e

if ! /usr/local/bin/wal-g --version > /dev/null 2>&1; then
    echo "wal-g binary failed --version probe"
    exit 1
fi

if [ ! -f /tmp/walg-alive ]; then
    echo "heartbeat file missing"
    exit 1
fi

age=$(($(date +%s) - $(stat -c %Y /tmp/walg-alive)))
if [ "$age" -gt 60 ]; then
    echo "heartbeat stale (${age}s old)"
    exit 1
fi

exit 0
