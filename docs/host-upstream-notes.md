# Host lifecycle source notes

Checked on 2026-09-15 for [issue #5](https://github.com/CraigDevJohnson/foundry/issues/5).
These notes explain the upstream behavior behind the offline templates. They do
not establish that the existing host has passed adoption checks.

## Foundry startup and shutdown

Foundry documents `node main.js --dataPath=...` for version 13 and newer. Version
14 requires Node 24. The installation guide recommends systemd for a standalone
installation. This supports preserving the observed Foundry `14.366.0` and Node
`24.18.1` versions while changing supervision. It does not establish the origin
or digest of either installed artifact. See the
[Foundry installation guide](https://foundryvtt.com/article/installation/).

The configuration guide documents `--dataPath` and `--port`. It documents
`sslCert` and `sslKey` as `Config/options.json` directives, with both required
to enable HTTPS. The published CLI listing does not establish support for
`--sslCert` or `--sslKey`. The conservative contract uses the documented CLI
flags and validates certificate paths in the existing configuration. Changing
settings through Setup writes `options.json` and shuts down Foundry, so a
readiness check must observe the actual process and connection. See
[Foundry application configuration](https://foundryvtt.com/article/configuration/).

Foundry's Linux backup instructions require closing Foundry and confirming that
its process has exited before archiving `Data` and `Config`. Its restore
instructions also require Foundry to be closed. See
[Foundry user-data backup and restore](https://foundryvtt.com/article/user-data-backup/).

The reviewed public Foundry guides do not specify a version-14 guarantee for
database shutdown on `SIGTERM` or `SIGINT`. Node's default signal handling is
not such a guarantee: applications can replace those handlers. See
[Node process signals](https://nodejs.org/api/process.html#signal-events).
Therefore, graceful shutdown of the licensed pinned package remains part of
the authorized live proof in issue #10. Offline tests can prove that a timeout
blocks further work and does not force-kill the application.

## systemd lifecycle semantics

The upstream systemd v252 manual defines `KillMode=control-group` to act on the
service's processes. `KillSignal` defaults to `SIGTERM`. Without an override,
systemd escalates to `SIGKILL` after the stop timeout. `SendSIGKILL=no` disables
that escalation and prevents restart while prior processes remain in a
`control-group` or `mixed` service. See
[systemd v252 kill documentation](https://github.com/systemd/systemd/blob/v252/man/systemd.kill.xml).

`TimeoutStopSec` bounds both an `ExecStop` command and the service's subsequent
exit. An `ExecStop` command that merely sends a signal without waiting can cause
an unclean stop. Without `ExecStop`, systemd sends the configured stop signal.
See [systemd v252 service documentation](https://github.com/systemd/systemd/blob/v252/man/systemd.service.xml).

The resulting safety rule is a project choice: a failed stop, remaining cgroup
process, or remaining application listener prevents restart and prevents the
caller from declaring data quiescent. The wrapper must not translate a timeout
into a successful stop. An unmanaged listener is never adopted or signaled by
the normal lifecycle commands.

These references establish the selected directives' semantics, not the
installed host's systemd version. Adoption must record and verify that version.

## Renewal when the host is off

`Persistent=true` catches missed `OnCalendar` events when a timer starts again.
It does not persist monotonic timers, and a catch-up run still obeys
`RandomizedDelaySec`. `OnBootSec` is relative to boot; if its deadline is already
past when activated, it fires immediately. Calendar timers also order after
the time-setting targets. See
[systemd v252 timer documentation](https://github.com/systemd/systemd/blob/v252/man/systemd.timer.xml).

A timer is not a startup check. The project contract needs a synchronous
certificate check before Foundry starts, after network and clock readiness.
An expired certificate can then be renewed on the next authorized host start.
Periodic renewal must not interrupt an active session through a restart hook.

Certbot `renew` exits successfully both after renewal and when nothing is due.
Deploy hooks run after successful issuance, but a hook failure does not directly
force a nonzero Certbot exit status. Renewal can invoke saved hooks and
executables in the renewal-hook directories. The standalone HTTP challenge
requires Internet reachability on port 80. These are reasons to inventory the
existing lineage, authenticator, and hooks before adoption and independently
validate the resulting certificate. See the
[Certbot renewal guide](https://eff-certbot.readthedocs.io/en/stable/using.html#renewing-certificates).

Certbot v2.11.0 implements `--no-random-sleep-on-renew` as a hidden flag. Its
renewal code otherwise delays noninteractive renewal by up to eight minutes.
This explains why bounded boot renewal needs the flag, but does not select a
Certbot version for the host. See the
[Certbot v2.11.0 argument parser](https://github.com/certbot/certbot/blob/v2.11.0/certbot/certbot/_internal/cli/__init__.py)
and [renewal code](https://github.com/certbot/certbot/blob/v2.11.0/certbot/certbot/_internal/renewal.py).

## Certificate checks

OpenSSL `verify` supports chain verification, hostname verification, server
purpose, and certificate-time checks. Trust roots must come from the host's
approved CA bundle, not the certificate under test. See
[OpenSSL verification](https://docs.openssl.org/3.0/man1/openssl-verify/) and
[verification options](https://docs.openssl.org/3.0/man1/openssl-verification-options/).

`openssl x509 -checkend` tests remaining lifetime only. A separate check must
reject a future `notBefore`, an untrusted chain, or the wrong hostname. OpenSSL
can export the certificate's public key and the private key's public component
for comparison, including non-RSA keys. See
[OpenSSL x509](https://docs.openssl.org/3.0/man1/openssl-x509/) and
[OpenSSL pkey](https://docs.openssl.org/3.0/man1/openssl-pkey/).

The project checks the staged certificate and key for readability as the
service user. It validates certificate files for a minimum remaining lifetime
and makes a locally routed HTTPS request with curl's normal TLS verification.
That request does not compare the served certificate's fingerprint with the
staged certificate or apply the file check's minimum remaining lifetime to the
served certificate. The live adoption check must record that comparison after
activation. Copying a renewed file does not prove that the running process has
loaded it.

## Reproducible operating-system inputs

Amazon Linux 2023 AMIs lock package installation to a repository release.
Existing AMIs do not automatically receive newer updates. The public latest-AMI
SSM parameter changes as releases arrive. Reproduction therefore needs an
explicit AMI ID and repository release, plus the recorded package versions and
artifact digests. See
[AL2023 deterministic upgrades](https://docs.aws.amazon.com/linux/al2023/ug/deterministic-upgrades.html).

The baseline does not contain an AMI ID, exact OS release, filesystem UUID,
numeric user/group IDs, Certbot version, or executable digests. Those inputs
remain explicit adoption prerequisites. Guessing values would violate the
requirement to preserve the current installation.
