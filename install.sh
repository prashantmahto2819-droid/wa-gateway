#!/usr/bin/env bash
# Vivid Relay one-command installer for a fresh Ubuntu/Debian VPS.
#   git clone https://github.com/prashantmahto2819-droid/wa-gateway.git
#   cd wa-gateway && sudo ./install.sh wa.yourdomain.com
set -euo pipefail

cd "$(dirname "$0")"
say() { printf '\n\033[1;32m==> %s\033[0m\n' "$*"; }
die() { printf '\n\033[1;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "sudo से चलाएँ: sudo ./install.sh your.domain.com"

DOMAIN="${1:-}"
if [ -z "$DOMAIN" ] && [ -f .env ]; then DOMAIN="$(grep -E '^DOMAIN=' .env | cut -d= -f2- || true)"; fi
if [ -z "$DOMAIN" ] || [ "$DOMAIN" = "wa.example.com" ]; then
  read -rp "Domain (जैसे wa.example.com, DNS A record इस VPS के IP पर): " DOMAIN </dev/tty
fi
[ -n "$DOMAIN" ] || die "domain ज़रूरी है"

say "Docker जाँच रहे हैं"
if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh
fi
docker compose version >/dev/null 2>&1 || die "docker compose plugin नहीं मिला"
systemctl enable --now docker >/dev/null 2>&1 || true

# Building needs ~1.5 GB RAM; small VPS get a swap file.
mem_kb=$(awk '/MemTotal/ {print $2}' /proc/meminfo)
if [ "$mem_kb" -lt 2000000 ] && ! swapon --show | grep -q .; then
  say "कम RAM है — 2GB swap file बना रहे हैं"
  fallocate -l 2G /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=2048
  chmod 600 /swapfile && mkswap /swapfile >/dev/null && swapon /swapfile
  grep -q '^/swapfile' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi

if command -v ufw >/dev/null 2>&1 && ufw status | grep -q active; then
  ufw allow 80/tcp >/dev/null; ufw allow 443/tcp >/dev/null
fi

if [ ! -f .env ]; then
  say ".env बना रहे हैं"
  cp .env.example .env
  key="$(openssl rand -hex 24 2>/dev/null || head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  secret="$(openssl rand -hex 24 2>/dev/null || head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"
  sed -i "s|^API_KEY=.*|API_KEY=${key}|; s|^WEBHOOK_SECRET=.*|WEBHOOK_SECRET=${secret}|" .env
  chmod 600 .env
fi
sed -i "s|^DOMAIN=.*|DOMAIN=${DOMAIN}|" .env

say "Build और start (पहली बार 5–10 मिनट लग सकते हैं)"
VERSION="$(git rev-parse --short HEAD 2>/dev/null || echo local)" docker compose up -d --build

say "Health check"
for i in $(seq 1 30); do
  if docker compose exec -T gateway wget -qO- http://127.0.0.1:8080/health >/dev/null 2>&1; then ok=1; break; fi
  sleep 2
done
[ "${ok:-}" = 1 ] || die "gateway शुरू नहीं हुआ — देखें: docker compose logs gateway"

API_KEY="$(grep -E '^API_KEY=' .env | cut -d= -f2-)"
cat <<EOF

✅ Vivid Relay चालू है!

  Website   : https://${DOMAIN}/
  पहला setup: https://${DOMAIN}/setup   (यहाँ यह master key डालकर अपना owner account बनाएँ)
  Master key: ${API_KEY}

अगला क़दम: /setup पर account बनाएँ → Dashboard → Numbers → नंबर जोड़ें → फ़ोन से QR scan करें।
HTTPS certificate बनने में 1–2 मिनट लग सकते हैं।
Logs: docker compose logs -f gateway     Update: sudo ./update.sh
EOF
