#!/bin/bash
# First-boot setup for the Foundry host (infra/host.tf, refactor Phase 7).
# cloud-init runs it once, as root. It holds no secrets and never formats,
# mounts or writes the data volume: that volume is formatted once by hand and
# then restored from the old host (infra/README.md). This is an OpenTofu
# template: a literal dollar-brace would need escaping, so the shell code
# avoids that form.
set -euxo pipefail

# --- Packages, from the AL2023 repositories (not NodeSource) ---
# The Elastic IP association can swap the public address while this runs, so
# retry instead of failing on one dropped connection.
for attempt in 1 2 3 4 5; do
  if dnf install -y nodejs24 certbot nginx; then
    break
  fi
  if [ "$attempt" -eq 5 ]; then
    echo "dnf install failed after $attempt attempts" >&2
    exit 1
  fi
  sleep 30
done

# --- Node.js 24 ---
# AL2023 installs the namespaced node-24 and points the virtual `node` name at
# it through alternatives. foundry.service calls the namespaced binary; pin
# `node` too (manual mode), so interactive use and restored scripts get 24 even
# if another nodejs package is installed later.
NODE_BIN='${node_bin}'
if [ ! -x "$NODE_BIN" ]; then
  echo "nodejs24 did not install $NODE_BIN" >&2
  exit 1
fi
if alternatives --display node >/dev/null 2>&1; then
  alternatives --set node "$NODE_BIN"
else
  ln -sfn "$NODE_BIN" /usr/bin/node
fi
case "$(/usr/bin/node --version)" in
  v24.*) ;;
  *)
    echo "/usr/bin/node is not Node.js 24" >&2
    exit 1
    ;;
esac

# --- Service account ---
# The same IDs as the old host, so restored ownership lines up. tar restores
# by name anyway and the restore ends with chown -R, so if an ID is taken on
# this AMI, let the system choose instead of failing.
FOUNDRY_ID=992
if ! getent group foundry >/dev/null; then
  if getent group "$FOUNDRY_ID" >/dev/null; then
    echo "GID $FOUNDRY_ID is taken; groupadd chooses one" >&2
    groupadd --system foundry
  else
    groupadd --system --gid "$FOUNDRY_ID" foundry
  fi
fi
if ! getent passwd foundry >/dev/null; then
  if getent passwd "$FOUNDRY_ID" >/dev/null; then
    echo "UID $FOUNDRY_ID is taken; useradd chooses one" >&2
    useradd --system --gid foundry --home-dir /home/foundry --create-home --shell /usr/sbin/nologin foundry
  else
    useradd --system --uid "$FOUNDRY_ID" --gid foundry --home-dir /home/foundry --create-home --shell /usr/sbin/nologin foundry
  fi
fi
chmod 0750 /home/foundry
if id ec2-user >/dev/null 2>&1; then
  usermod -aG foundry ec2-user
fi

# --- Data volume mount point ---
# Mount the data volume by its NVMe volume-ID link, never by label or UUID: a
# restore-test volume made from its snapshot has the same label and UUID.
# nofail: the host boots normally before the volume is attached or formatted.
DATA_DEVICE='${data_volume_device}'
mkdir -p /foundry
if ! grep -q '[[:space:]]/foundry[[:space:]]' /etc/fstab; then
  echo "$DATA_DEVICE /foundry ext4 defaults,nofail 0 2" >>/etc/fstab
fi

# --- Foundry unit (D15: plain systemd unit) ---
# Installed but not enabled: the restore step enables it once /foundry holds
# the restored app and data.
cat >/etc/systemd/system/foundry.service <<'UNIT'
${chomp(foundry_unit)}
UNIT
chmod 0644 /etc/systemd/system/foundry.service

# --- Reverse proxy (ADR 0008) ---
# nginx answers https://foundry.craigdevjohnson.com on 443 and hands requests
# to Foundry on 127.0.0.1:30000. The script is idempotent: at first boot there
# is no certificate, so it serves port 80 only (certbot's challenge and the
# redirect); after the certificate is issued it is run again by hand and adds
# port 443 (infra/README.md).
cat >/usr/local/sbin/foundry-proxy-setup <<'PROXY'
${chomp(proxy_setup)}
PROXY
chmod 0755 /usr/local/sbin/foundry-proxy-setup
/usr/local/sbin/foundry-proxy-setup

# --- Certificate deploy hook ---
# The script lives outside /etc/letsencrypt, which the restore overwrites; the
# hook directory only links to it. It reloads nginx, which serves the
# certificate from certbot's lineage, so a renewal disconnects nobody.
cat >/usr/local/sbin/foundry-cert-deploy <<'HOOK'
${chomp(cert_deploy)}
HOOK
chmod 0755 /usr/local/sbin/foundry-cert-deploy
install -d -m 0755 /etc/letsencrypt/renewal-hooks/deploy
ln -sfn /usr/local/sbin/foundry-cert-deploy /etc/letsencrypt/renewal-hooks/deploy/foundry.sh

# --- Renewal timer: daily at 10:00 UTC with 15 minutes of jitter ---
# Before the restore there is no certificate, so a run does nothing. The host
# may be off at 10:00 UTC between sessions, so Persistent=true runs a missed
# renewal at the next boot instead of waiting for a 10:00 UTC the host is up
# for. It is set here rather than left to the certbot package's own timer.
install -d -m 0755 /etc/systemd/system/certbot-renew.timer.d
cat >/etc/systemd/system/certbot-renew.timer.d/override.conf <<'TIMER'
[Timer]
OnCalendar=
OnCalendar=*-*-* 10:00:00 UTC
Persistent=true
RandomizedDelaySec=15m
TIMER

systemctl daemon-reload
systemctl enable --now certbot-renew.timer

echo "foundry first-boot setup complete"
