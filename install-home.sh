#!/usr/bin/env bash
# Vivid Relay on a home server (Ubuntu, e.g. a VMware VM) behind a normal
# home internet connection: no public IP or router ports needed — a free
# Cloudflare Tunnel brings https://<hostname> to the gateway.
#
#   1. Cloudflare dashboard → Zero Trust → Networks → Tunnels → Create tunnel
#      (Cloudflared) → copy the token (the long text after --token).
#   2. In the tunnel: Public hostname → e.g. relay2.vividbrandify.com →
#      Service: HTTP  gateway:8080
#   3. sudo ./install-home.sh relay2.vividbrandify.com <TUNNEL_TOKEN>
set -euo pipefail
cd "$(dirname "$0")"
say() { printf '\n\033[1;32m==> %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "sudo से चलाएँ: sudo ./install-home.sh relay2.yourdomain.com TUNNEL_TOKEN"
DOMAIN="${1:-}"
TOKEN="${2:-}"
[ -n "$DOMAIN" ] || read -rp "Hostname (जैसे relay2.vividbrandify.com): " DOMAIN </dev/tty
[ -n "$TOKEN" ] || read -rp "Cloudflare Tunnel token: " TOKEN </dev/tty
[ -n "$DOMAIN" ] && [ -n "$TOKEN" ] || die "hostname और tunnel token दोनों ज़रूरी हैं"

say "Docker जाँच रहे हैं"
if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh
fi
docker compose version >/dev/null 2>&1 || die "docker compose plugin नहीं मिला"
systemctl enable --now docker >/dev/null 2>&1 || true

if [ ! -f .env ]; then
  say ".env बना रहे हैं"
  cp .env.example .env
  key="$(openssl rand -hex 24 2>/dev/null || head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  secret="$(openssl rand -hex 24 2>/dev/null || head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  sed -i "s|^API_KEY=.*|API_KEY=${key}|; s|^WEBHOOK_SECRET=.*|WEBHOOK_SECRET=${secret}|" .env
  chmod 600 .env
fi
setkv() { grep -q "^$1=" .env && sed -i "s|^$1=.*|$1=$2|" .env || echo "$1=$2" >> .env; }
setkv DOMAIN "$DOMAIN"
setkv PUBLIC_URL "https://$DOMAIN"
setkv TUNNEL_TOKEN "$TOKEN"
setkv COMPOSE_FILE docker-compose.home.yml   # so ./update.sh and docker compose use the home setup

say "Build और start (पहली बार 5–10 मिनट)"
VERSION="$(git rev-parse --short HEAD 2>/dev/null || echo local)" docker compose up -d --build

say "Health check"
for i in $(seq 1 30); do
  if docker compose exec -T gateway wget -qO- http://127.0.0.1:8080/health >/dev/null 2>&1; then ok=1; break; fi
  sleep 2
done
[ "${ok:-}" = 1 ] || die "gateway शुरू नहीं हुआ — देखें: docker compose logs gateway"

API_KEY="$(grep -E '^API_KEY=' .env | cut -d= -f2-)"
cat <<MSG

✅ Vivid Relay (घर वाला server) चालू है!

  Website   : https://${DOMAIN}/   (Cloudflare Tunnel से)
  पहला setup: https://${DOMAIN}/setup
  Master key: ${API_KEY}
  इसी मशीन पर: http://127.0.0.1:8090/

Tunnel की हालत: docker compose logs -f tunnel
Logs: docker compose logs -f gateway     Update: sudo ./update.sh
MSG
