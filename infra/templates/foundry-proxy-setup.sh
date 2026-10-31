#!/usr/bin/env bash
# Set up nginx as Foundry's reverse proxy (ADR 0008): https://foundry.craigdevjohnson.com
# on port 443 hands requests to Foundry on 127.0.0.1:30000, and port 80 answers
# certbot's challenge and redirects everything else to HTTPS. Installed by user
# data as /usr/local/sbin/foundry-proxy-setup and run at first boot; run it
# again by hand after the first certificate is issued (infra/README.md), and
# once on a host built before it existed (docs/operations.md).
#
# It is idempotent and safe to repeat: it writes the same files each time,
# serves port 443 only once the certificate exists, and reloads nginx without
# dropping connections. It never writes the data volume.
set -euo pipefail

hostname=foundry.craigdevjohnson.com
lineage=/etc/letsencrypt/live/$hostname
renewal_conf=/etc/letsencrypt/renewal/$hostname.conf
webroot=/var/lib/foundry-acme
http_conf=/etc/nginx/conf.d/foundry-http.conf
https_conf=/etc/nginx/conf.d/foundry-https.conf
options_json=/foundry/foundrydata/Config/options.json

# --- nginx, from the AL2023 repositories ---
if ! rpm -q nginx >/dev/null 2>&1; then
  for attempt in 1 2 3 4 5; do
    if dnf install -y nginx; then
      break
    fi
    if [ "$attempt" -eq 5 ]; then
      echo "foundry-proxy-setup: dnf install nginx failed after $attempt attempts" >&2
      exit 1
    fi
    sleep 30
  done
fi

# Certbot's webroot: the challenge files go under .well-known/acme-challenge.
install -d -m 0755 "$webroot"

# --- The whole nginx configuration, so no packaged default server answers ---
cat >/etc/nginx/nginx.conf <<'CONF'
# Written by /usr/local/sbin/foundry-proxy-setup (foundry: infra/templates/foundry-proxy-setup.sh).
# Edits here are overwritten the next time it runs.
user nginx;
worker_processes auto;
error_log /var/log/nginx/error.log notice;
pid /run/nginx.pid;

include /usr/share/nginx/modules/*.conf;

events {
    worker_connections 1024;
}

http {
    include /etc/nginx/mime.types;
    default_type application/octet-stream;
    access_log /var/log/nginx/access.log combined;
    sendfile on;
    server_tokens off;

    # Foundry's asset uploads; nginx's default of 1 MiB would refuse them.
    client_max_body_size 300M;

    # WebSocket upgrades (Foundry's real-time connection) pass through; other
    # requests keep an ordinary connection to Foundry.
    map $http_upgrade $connection_upgrade {
        default upgrade;
        ''      close;
    }

    include /etc/nginx/conf.d/*.conf;
}
CONF
chmod 0644 /etc/nginx/nginx.conf

cat >"$http_conf" <<CONF
# Port 80: certbot's HTTP-01 challenge from the webroot, everything else to HTTPS.
server {
    listen 80;
    server_name $hostname;

    location /.well-known/acme-challenge/ {
        root $webroot;
    }

    location / {
        return 301 https://$hostname\$request_uri;
    }
}
CONF
chmod 0644 "$http_conf"

if [ -r "$lineage/fullchain.pem" ] && [ -r "$lineage/privkey.pem" ]; then
  cat >"$https_conf" <<CONF
# Port 443: TLS ends here, and Foundry answers on 127.0.0.1:30000 over HTTP
# with proxyPort 443 and proxySSL true in its options.json.
server {
    listen 443 ssl;
    server_name $hostname;

    ssl_certificate $lineage/fullchain.pem;
    ssl_certificate_key $lineage/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 1d;
    ssl_session_tickets off;

    location / {
        proxy_pass http://127.0.0.1:30000;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection \$connection_upgrade;
    }
}
CONF
  chmod 0644 "$https_conf"
  echo "foundry-proxy-setup: serving https://$hostname from $lineage"
else
  rm -f "$https_conf"
  echo "foundry-proxy-setup: no certificate in $lineage yet; serving port 80 only. Issue one, then run this again."
fi

# AL2023 ships SELinux permissive; if it is ever enforcing, nginx needs leave
# to connect to Foundry's port.
if command -v getenforce >/dev/null 2>&1 && [ "$(getenforce)" = Enforcing ]; then
  setsebool -P httpd_can_network_connect on
fi

nginx -t
systemctl enable --now nginx
systemctl reload nginx

# --- Certbot answers the challenge through nginx from now on ---
# Standalone can't bind port 80 once nginx holds it. A certificate issued with
# --webroot already renews this way; one issued with --standalone (the host
# built on 2026-09-29) is switched once. Certbot holds a lock while it runs,
# so this is skipped inside its own hooks (RENEWED_LINEAGE is set there).
if [ -f "$renewal_conf" ] && [ -z "${RENEWED_LINEAGE:-}" ] &&
   ! grep -q '^authenticator = webroot$' "$renewal_conf"; then
  echo "foundry-proxy-setup: switching certbot's renewal to the webroot authenticator"
  certbot reconfigure --cert-name "$hostname" --non-interactive \
    --webroot --webroot-path "$webroot"
fi

# --- Foundry's side, which this script never writes (it is on the data volume) ---
if [ -f "$options_json" ] && [ ! -L "$options_json" ]; then
  if ! python3 -c '
import json, os, sys
with os.fdopen(os.open(sys.argv[1], os.O_RDONLY | os.O_NOFOLLOW)) as f:
    o = json.load(f)
ok = o.get("proxyPort") == 443 and o.get("proxySSL") is True and o.get("sslCert") is None and o.get("sslKey") is None
sys.exit(0 if ok else 1)
' "$options_json"; then
    echo "foundry-proxy-setup: $options_json still has Foundry serving TLS itself. Set proxyPort to 443, proxySSL to true and sslCert and sslKey to null, then restart foundry.service (docs/operations.md)." >&2
  fi
fi

echo "foundry-proxy-setup: done"
