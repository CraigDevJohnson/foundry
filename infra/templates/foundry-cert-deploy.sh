#!/usr/bin/env bash
# Certbot deploy hook for the Foundry host, installed by user data as
# /usr/local/sbin/foundry-cert-deploy and linked from
# /etc/letsencrypt/renewal-hooks/deploy/foundry.sh.
#
# Copies the renewed certificate to where options.json expects it (sslCert
# foundry.pem and sslKey foundry.key, relative to Config), then restarts
# Foundry if it is running. Certbot sets RENEWED_LINEAGE; for a manual run,
# pass the lineage directory or rely on the default below.
set -euo pipefail

lineage="${RENEWED_LINEAGE:-${1:-/etc/letsencrypt/live/foundry.craigdevjohnson.com}}"
config_dir=/foundry/foundrydata/Config

for f in fullchain.pem privkey.pem; do
  if [[ ! -r "$lineage/$f" ]]; then
    echo "foundry-cert-deploy: $lineage/$f not found" >&2
    exit 1
  fi
done
if [[ ! -d "$config_dir" ]]; then
  echo "foundry-cert-deploy: $config_dir not found; is /foundry mounted and restored?" >&2
  exit 1
fi

install -o foundry -g foundry -m 0644 "$lineage/fullchain.pem" "$config_dir/foundry.pem"
install -o foundry -g foundry -m 0600 "$lineage/privkey.pem" "$config_dir/foundry.key"

systemctl try-restart foundry.service
