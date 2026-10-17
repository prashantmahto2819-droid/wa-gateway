#!/usr/bin/env bash
# Update the gateway.
#   sudo ./update.sh              -> pull latest code from GitHub and rebuild
#   sudo ./update.sh --whatsmeow  -> also move to the newest whatsmeow (when WhatsApp changed something)
# Your linked numbers stay linked: login data lives in the gw_data volume.
set -euo pipefail
cd "$(dirname "$0")"

git pull --ff-only || echo "git pull skipped (local changes?)"

if [ "${1:-}" = "--whatsmeow" ]; then
  echo "==> whatsmeow को latest पर ले जा रहे हैं"
  docker run --rm -v "$PWD":/src -w /src golang:1.27-trixie sh -c \
    'go get go.mau.fi/whatsmeow@latest && go mod tidy && go test ./...'
  echo "go.mod बदल गया है — काम ठीक चले तो इसे GitHub पर commit कर दें।"
fi

VERSION="$(git rev-parse --short HEAD 2>/dev/null || echo local)" docker compose up -d --build
docker image prune -f >/dev/null 2>&1 || true
echo "✅ Updated. Logs: docker compose logs -f gateway"
