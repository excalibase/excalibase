#!/usr/bin/env bash
# WAL-G sidecar entrypoint. Sleeps forever — the platform-side
# scheduler is responsible for invoking `docker exec sidecar wal-g
# backup-push /walarchive` on cadence. The sidecar exists to:
#   1. Host the wal-g binary on the same Docker network as the pg
#      container so archive_command='wal-g wal-push %p' works.
#   2. Share the walarchive volume so wal-g can read both the pg
#      data dir mount and the WAL queue.
#   3. Survive pg restarts independently.
set -euo pipefail

# Required env: WALG_S3_PREFIX, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY.
# Optional: AWS_ENDPOINT (R2 / S3-compatible), WALG_LIBSODIUM_KEY,
# WALG_COMPRESSION_METHOD (default lz4), AWS_REGION (default auto).
: "${WALG_S3_PREFIX:?WALG_S3_PREFIX required}"
: "${AWS_ACCESS_KEY_ID:?AWS_ACCESS_KEY_ID required}"
: "${AWS_SECRET_ACCESS_KEY:?AWS_SECRET_ACCESS_KEY required}"
export AWS_REGION="${AWS_REGION:-auto}"
export WALG_COMPRESSION_METHOD="${WALG_COMPRESSION_METHOD:-lz4}"

echo "[walg-sidecar] WALG_S3_PREFIX=$WALG_S3_PREFIX region=$AWS_REGION compression=$WALG_COMPRESSION_METHOD"

# Touch a heartbeat file so healthcheck.sh knows we're alive.
touch /tmp/walg-alive

# Trap SIGTERM so the platform's stop-with-grace works.
term_handler() {
    echo "[walg-sidecar] SIGTERM received, draining queue..."
    # Best-effort: try to push any pending WAL segments before exit.
    if [ -d /walarchive ]; then
        for seg in /walarchive/*; do
            [ -f "$seg" ] || continue
            wal-g wal-push "$seg" || true
        done
    fi
    exit 0
}
trap term_handler SIGTERM

# Idle loop. Backup invocations come from `docker exec` from the
# platform server's scheduler; the loop here just keeps the
# container alive and the heartbeat fresh.
while true; do
    touch /tmp/walg-alive
    sleep 30 &
    wait $!
done
