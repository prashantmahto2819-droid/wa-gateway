#!/usr/bin/env bash
# Makes a consistent backup of everything (logins, contacts, campaigns, media, licence key).
#   sudo ./backup.sh            -> backups/vivid-relay-YYYYmmdd-HHMM.tar.gz
# Restore: stop the stack, copy the files back into the gw_data volume, start again.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p backups
stamp=$(date +%Y%m%d-%H%M)
docker compose exec -T gateway sh -c 'cd /data && rm -rf /tmp/bk && mkdir -p /tmp/bk &&
  sqlite3 gateway.db ".backup /tmp/bk/gateway.db" &&
  cp -r media licence_ed25519.seed /tmp/bk/ 2>/dev/null || true; tar -C /tmp/bk -czf /tmp/backup.tgz .'
docker compose cp gateway:/tmp/backup.tgz "backups/vivid-relay-$stamp.tar.gz"
ls -1t backups/vivid-relay-*.tar.gz | tail -n +15 | xargs -r rm -f   # keep the last 14
echo "✅ backups/vivid-relay-$stamp.tar.gz"
