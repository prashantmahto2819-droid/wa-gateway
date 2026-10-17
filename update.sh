#!/usr/bin/env bash
# Update the gateway.
#   sudo ./update.sh              -> pull latest code from GitHub and rebuild
#   sudo ./update.sh --whatsmeow  -> also move to the newest whatsmeow (when WhatsApp changed something)
# Your linked numbers stay linked: login data lives in the gw_data volume.
set -euo pipefail
cd "$(dirname "$0")"

# A previous --whatsmeow run edits go.mod locally; reset it so the pull
# works, and remember to apply the newest whatsmeow again afterwards.
BUMP=0
[ "${1:-}" = "--whatsmeow" ] && BUMP=1
git diff --quiet -- go.mod 2>/dev/null || BUMP=1
git checkout -- go.mod 2>/dev/null || true
rm -f go.sum
if ! git pull --ff-only; then
  echo "ERROR: git pull failed. 'git status' देखें — local बदलाव हैं तो उन्हें हटाएँ/commit करें।" >&2
  exit 1
fi

if [ "$BUMP" = 1 ]; then
  echo "==> whatsmeow को latest पर ले जा रहे हैं"
  docker run --rm -v "$PWD":/src -w /src golang:1.27-trixie sh -c \
    'go get go.mau.fi/whatsmeow@latest && go mod tidy && go test ./...'
  echo "go.mod बदल गया है — काम ठीक चले तो इसे GitHub पर commit कर दें।"
fi

VERSION="$(git rev-parse --short HEAD 2>/dev/null || echo local)" docker compose up -d --build
docker image prune -f >/dev/null 2>&1 || true
echo "✅ Updated. Logs: docker compose logs -f gateway"
