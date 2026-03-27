#!/usr/bin/env bash
set -euo pipefail

MAILR_DOMAIN="__MAILR_DOMAIN__"
MAILR_REPO="__MAILR_REPO__"

echo "==> Installing Docker..."
apt-get update -y
apt-get install -y ca-certificates curl gnupg lsb-release

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg

echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
  https://download.docker.com/linux/ubuntu $(lsb_release -cs) stable" \
  > /etc/apt/sources.list.d/docker.list

apt-get update -y
apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
systemctl enable docker
systemctl start docker

echo "==> Cloning mailr..."
if [ -d /opt/mailr ]; then
  cd /opt/mailr && git pull
else
  git clone "$MAILR_REPO" /opt/mailr
fi
cd /opt/mailr

echo "==> Configuring..."
MAILR_ADMIN_TOKEN=$(openssl rand -hex 32)

cat > .env <<EOF
MAILR_DOMAIN=${MAILR_DOMAIN}
MAILR_ADMIN_TOKEN=${MAILR_ADMIN_TOKEN}
EOF

echo "$MAILR_ADMIN_TOKEN" > /opt/mailr/.admin-token
chmod 600 /opt/mailr/.admin-token

echo "==> Starting mailr..."
docker compose up -d --build

echo "==> Waiting for health..."
for i in $(seq 1 30); do
  if curl -sf http://localhost:4802/health > /dev/null 2>&1; then
    echo "==> mailr is healthy!"
    echo ""
    echo "  Health:  https://${MAILR_DOMAIN}/health"
    echo "  API:     https://${MAILR_DOMAIN}/api"
    echo "  SMTP:    port 25"
    echo ""
    echo "  Admin token: sudo cat /opt/mailr/.admin-token"
    echo "  Update:      cd /opt/mailr && git pull && docker compose up -d --build"
    exit 0
  fi
  sleep 2
done

echo "ERROR: mailr did not become healthy within 60 seconds"
docker compose logs
exit 1
