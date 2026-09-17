#!/usr/bin/env bash
# Run as root from a reviewed checkout on the local deployment machine.
set -euo pipefail
cd "$(dirname "$0")/.."
test "$(id -u)" -eq 0
test -x /usr/local/bin/node
for account in human-worth human-worth-deploy human-worth-tunnel; do
  if ! id "$account" >/dev/null 2>&1; then
    useradd --system --user-group --home-dir "/var/lib/$account" --create-home --shell /usr/sbin/nologin "$account"
  fi
done
install -d -m 0755 -o human-worth-deploy -g human-worth-deploy /opt/human-worth /opt/human-worth/releases
install -d -m 0755 /usr/local/lib/human-worth
install -m 0644 ops/deploy.py /usr/local/lib/human-worth/deploy.py
install -m 0644 ops/systemd/human-worth.service ops/systemd/human-worth-deploy.service ops/systemd/human-worth-deploy.timer ops/systemd/human-worth-tunnel.service /etc/systemd/system/
install -d -m 0700 -o human-worth-tunnel -g human-worth-tunnel /var/lib/human-worth-tunnel
if [ ! -e /var/lib/human-worth-tunnel/id_ed25519 ]; then
  runuser -u human-worth-tunnel -- ssh-keygen -q -t ed25519 -N '' -C human-worth-tunnel -f /var/lib/human-worth-tunnel/id_ed25519
fi
printf '%s\n' 'human-worth-deploy ALL=(root) NOPASSWD: /usr/bin/systemctl restart human-worth.service, /usr/bin/systemctl stop human-worth.service' > /etc/sudoers.d/human-worth-deploy
chmod 0440 /etc/sudoers.d/human-worth-deploy
visudo -cf /etc/sudoers.d/human-worth-deploy
systemctl daemon-reload
systemctl enable human-worth.service human-worth-deploy.timer human-worth-tunnel.service
# Start only after ECS has the public key and the local known_hosts was verified.
printf '%s\n' 'Install verified gateway host key; then start the tunnel and deploy timer.'
