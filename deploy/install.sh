#!/usr/bin/env bash
# Build mrdns and install it under /opt/mrdns. Run as root from the repo root.
set -euo pipefail

PREFIX=/opt/mrdns
SVC_USER=mrdns
SVC_GROUP=mrdns

echo ">> building binary"
CGO_ENABLED=0 go build -trimpath -o ./dist/mrdns ./cmd/mrdns

echo ">> creating group/user ${SVC_USER}"
getent group "${SVC_GROUP}" >/dev/null || groupadd --system "${SVC_GROUP}"
id -u "${SVC_USER}" >/dev/null 2>&1 || \
  useradd --system --gid "${SVC_GROUP}" --home-dir "${PREFIX}" --shell /usr/sbin/nologin "${SVC_USER}"

echo ">> creating layout under ${PREFIX}"
install -d -m 0755 "${PREFIX}/bin" "${PREFIX}/etc"
install -d -o "${SVC_USER}" -g "${SVC_GROUP}" -m 0750 "${PREFIX}/var" "${PREFIX}/var/log"

echo ">> installing binary"
install -m 0755 ./dist/mrdns "${PREFIX}/bin/mrdns"

echo ">> installing config and secrets (existing files are left untouched)"
[ -f "${PREFIX}/etc/mrdns.yaml" ] || install -m 0640 -g "${SVC_GROUP}" configs/mrdns.example.yaml "${PREFIX}/etc/mrdns.yaml"
[ -f "${PREFIX}/etc/mrdns.env" ]  || install -m 0640 -g "${SVC_GROUP}" deploy/mrdns.env.example  "${PREFIX}/etc/mrdns.env"

echo ">> installing systemd unit"
install -m 0644 deploy/mrdns.service /etc/systemd/system/mrdns.service
systemctl daemon-reload

# On an upgrade, restart the running service so the new binary takes effect.
echo ">> restarting service if already running"
systemctl try-restart mrdns.service || true

cat <<EOF

Done. Next steps:
  1. Edit ${PREFIX}/etc/mrdns.env and set strong secrets:
       MRDNS_TOKEN=\$(openssl rand -hex 32)
       MRDNS_COOKIE_KEY=\$(openssl rand -hex 32)
  2. Edit ${PREFIX}/etc/mrdns.yaml (servers + settings).
  3. Place the SSH key and known_hosts in ${PREFIX}/etc/.
  4. Start it (first install):
       systemctl enable --now mrdns
  Zones are created in the web UI, not in the config file.
  (An already-running service was restarted automatically above.)
EOF
