---
status: accepted
date: 2026-10-30
---

# Serve Foundry on 443 through nginx on the host

Players open `https://foundry.craigdevjohnson.com`, with no port. nginx on
the host answers port 443, ends TLS with the Let's Encrypt certificate certbot
already keeps, and hands each request, WebSocket upgrades included, to the
Foundry VTT service on `127.0.0.1:30000`. Port 80 answers certbot's challenge
and redirects everything else to HTTPS. The security group opens 80 and 443
only: port 30000 is no longer reachable from outside, so the proxy is the one
way in ([#31](https://github.com/CraigDevJohnson/foundry/issues/31)).

## Why this and not Cloudflare or a load balancer

The issue left AWS against Cloudflare open until the architecture was
inspected. Before this change Foundry served HTTPS itself on 30000 from a
certificate certbot copied into its `Config`, the Cloudflare record was a
DNS-only A record for the Elastic IP (aws-setup decision D19), and nothing
else stood between players and the host. Three ways to drop the port were
weighed:

- **nginx on the host** (chosen). One package from the AL2023 repositories,
  about forty lines of configuration, no new AWS resource and no monthly
  cost. Everything stays in this repository: user data installs it on a
  rebuild, OpenTofu changes the security group, and the Deploy workflow
  applies it. The Cloudflare record stays DNS only, as D19 and the DNS
  runbook ask. A certificate renewal now reloads nginx instead of restarting
  Foundry, so renewing disconnects nobody. The cost is one more service on
  the host and a one-time, by-hand move on the host built before it.
- **Cloudflare's proxy with an origin rule** to port 30000, or a **Cloudflare
  Tunnel**. Either hides the origin and would let 30000 close too, but each
  moves the routing into Cloudflare's dashboard, which nothing in this
  repository manages, reverses D19, and brings the proxy's limits to Foundry
  (its upload size cap and its terms on non-web traffic). Nothing here needs
  what they add.
- **An Application or Network Load Balancer** with an ACM certificate. The
  cleanest AWS answer, and the only one that costs money every month, about
  as much again as the host for a game that runs a few hours a week. The
  operating priorities rule out an increase without a reason, and there is
  none.

## What changed

- `infra/templates/foundry-proxy-setup.sh`, installed by user data as
  `/usr/local/sbin/foundry-proxy-setup` and run at first boot: installs nginx,
  writes the whole `nginx.conf` (so no packaged default server answers), the
  port 80 server (challenge from `/var/lib/foundry-acme`, redirect to HTTPS),
  and the port 443 server once the certificate exists, then enables and
  reloads nginx. It is idempotent. On a host whose certificate was issued
  with certbot's standalone authenticator it runs `certbot reconfigure` once,
  switching the renewal to the webroot nginx serves, since standalone can no
  longer bind port 80.
- The deploy hook `foundry-cert-deploy` reloads nginx. It no longer copies
  the certificate into Foundry's `Config` or restarts Foundry.
- Foundry's `options.json` carries `proxyPort: 443`, `proxySSL: true` and
  `sslCert` and `sslKey` set to null, as Foundry's own reverse-proxy guide
  says, so it serves plain HTTP on 30000 and builds its links as
  `https://foundry.craigdevjohnson.com`. This is on the data volume, so the
  move sets it by hand once ([operations](../operations.md#the-reverse-proxy))
  and a rebuild inherits it.
- The `foundry-proxy-setup` SSM document (`infra/ci.tf`), which the Host
  control workflow's `proxy` action sends (`task host:proxy`): it installs
  the commit's two proxy scripts on the host, runs the setup, sets Foundry's
  options and restarts Foundry only if they changed, tests the renewal and
  checks `/join` through nginx on the host. User data runs only at first
  boot, so this is how the proxy reached the host built before it, and how a
  later change to the scripts reaches a built host, without a shell on it.
  It has no parameters, like `foundry-shutdown`, so the Host control role
  still can't run arbitrary commands.
- The security group opens 80 and 443 from anywhere and nothing else. The
  controller probes `https://foundry.craigdevjohnson.com/join`, and the
  Taskfile, the live-checks script and the docs use the same address.

Foundry's shutdown script still watches port 30000, where Foundry listens as
before. nginx keeps running when Foundry is stopped and answers 502 then,
which the controller reads as not responding, as it did a refused connection.

The offline host contract ([docs/host-contract.md](../host-contract.md)) still
describes Foundry serving TLS itself on 30000. It is the old host's contract
and the workloads host follows only its unit template, so it is left as
history.
