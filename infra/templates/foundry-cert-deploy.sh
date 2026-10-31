#!/usr/bin/env bash
# Certbot deploy hook for the Foundry host, installed by user data as
# /usr/local/sbin/foundry-cert-deploy and linked from
# /etc/letsencrypt/renewal-hooks/deploy/foundry.sh.
#
# nginx serves the certificate straight from certbot's lineage (ADR 0008), so
# a renewal only needs nginx to reload, which keeps open connections: nobody
# playing is disconnected. Foundry itself no longer holds a copy. Certbot sets
# RENEWED_LINEAGE; for a manual run, pass the lineage directory or rely on the
# default below.
set -euo pipefail

lineage="${RENEWED_LINEAGE:-${1:-/etc/letsencrypt/live/foundry.craigdevjohnson.com}}"

for f in fullchain.pem privkey.pem; do
  if [[ ! -r "$lineage/$f" ]]; then
    echo "foundry-cert-deploy: $lineage/$f not found" >&2
    exit 1
  fi
done
if [[ ! -f /etc/nginx/conf.d/foundry-https.conf ]]; then
  echo "foundry-cert-deploy: nginx is not serving port 443 yet; run /usr/local/sbin/foundry-proxy-setup" >&2
  exit 1
fi

nginx -t
systemctl reload nginx
