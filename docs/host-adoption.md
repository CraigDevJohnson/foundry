# Adopt and roll back the existing Foundry host

Use this runbook during the separately authorized live gate in
[issue #10](https://github.com/CraigDevJohnson/foundry/issues/10). Issue #5 supplies
offline templates and tests only. It does not authorize host access, service
interruption, certificate issuance, installation, snapshots, or other AWS
changes. This repository has no installer that performs adoption.

Before executing these steps, record Craig's approval of the concrete resource
plan, incremental cost, maintenance window, temporary recovery resources, and
rollback procedure. Confirm that play and GM preparation have ended. Follow the
[host contract](host-contract.md) for paths, commands, and failure results.

## 1. Capture the current installation

Treat the [2026-09-08 baseline](hosting-baseline.md) as a starting point. Recheck
the following through the approved host access path and store the results in a
private adoption record. Keep configuration, credentials, license material, and
private keys out of Git and public logs.

1. Record the AWS account, Region, Availability Zone, instance ID, AMI ID,
   architecture, security groups, instance profile, Elastic IP association, DNS
   records, and both EBS volume IDs. Record each volume's encryption key,
   attachment, size, filesystem UUID, mount options, and termination flag. Save
   the existing snapshot IDs and retention instructions.
2. Capture the exact Amazon Linux release, kernel and installed RPM versions.
   Record the repository release used to obtain those packages. Resolve and
   retain the matching ARM64 AMI and required package artifacts before treating
   the setup as reconstructible.
3. Record `/usr/bin/node --version`, the resolved binary path, its SHA-256, and
   its package provenance. Record Foundry's package version and SHA-256 for
   `/foundry/foundryvtt/main.js`. Preserve the complete installed application and
   the licensed release archive, with its SHA-256 and private recovery location.
   Preserve Node `v24.18.1` and Foundry package `14.366.0`; if the current host
   differs, stop and review that drift before proceeding. Do not upgrade.
4. Capture `/etc/fstab`, `findmnt` and block-device output, mount UUID and type,
   and space/inode usage. Verify `/foundry` is the expected dedicated ext4
   volume. Do not create a data directory beneath an absent mount.
5. Record the numeric UID and GID for `foundry:foundry`, all relevant directory
   modes, recursive ownership, ACLs, extended attributes, and any SELinux
   labels. Save the metadata in a form that can restore each original path.
   Record `ec2-user`'s identity separately because it owns the observed legacy
   process. Do not guess or reassign an existing numeric ID.
6. Enumerate both world directory IDs under
   `/foundry/foundrydata/Data/worlds`. Record their world, system, and module
   versions, configured default world, and required assets. Inspect symlinks
   and configuration for files outside `Data` and `Config`, including absolute
   asset paths and custom scripts. Add every required external file and its
   restore destination to the recovery inventory.
7. Inventory all existing files in `/foundry/foundrydata/Backups`, with their
   sizes and hashes. Preserve archives and companion manifests together. The
   baseline's file counts and timestamps do not prove backup coverage.
8. Capture the serving PID, process start time, executable, complete argument
   vector, effective UID/GID, parent process, cgroup, and all IPv4/IPv6 listeners
   on port `30000`. Save the exact existing launch environment and working
   directory privately. Confirm which process owns each listener.
9. Save the bytes and metadata of `/etc/systemd/system/foundry.service`, its
   drop-ins and effective configuration, and its enabled/active states. Capture
   `/foundry/start_foundry.sh`, `/foundry/stop_foundry.sh`,
   `/foundry/renew_cert.sh`, relevant cron jobs, login/session launchers, and
   renewal unit files and enablement. Inspect the scripts without running them.
10. Save the active TLS paths and certificate fingerprint, names, chain and
    dates. Preserve the complete private Certbot state, renewal configuration,
    account material, authenticator credentials, hooks, plugin versions, and
    any relevant web server configuration. Record how renewal authenticates
    while Foundry is stopped and how each credential will be restored.

Populate a private copy of `host/host.example.json` from this record. Supply all
required immutable identifiers and hashes. Keep unknown values unresolved; the
renderer must refuse them. Preserve the manifest, repository commit, generated
file hashes, and artifact locations with the recovery inventory.

## 2. Preserve a consistent recovery copy before changing ownership

1. Reserve enough space at an independent approved recovery destination. Do not
   use the live `/foundry` volume as the only copy. Include the current root
   configuration, complete application, `Data`, `Config`, `Backups`, external
   dependencies, and the private inventory. Preserve file metadata and links.
2. Confirm that no player or GM is writing. Recheck the legacy process identity
   against step 1 immediately before stopping it. If its PID, start time,
   executable, arguments, owner, or listener ownership changed, stop the
   procedure and investigate.
3. Use the verified application's graceful shutdown method for that exact
   process. If the reviewed method is a signal, send `SIGTERM` only to the
   confirmed serving PID and wait up to 90 seconds. Do not execute the legacy
   stop script without reviewing its targets. Do not use `killall`, a broad
   `pkill`, `SIGKILL`, or an EC2 stop to make this step pass.
4. Prove that the process has exited, its write-capable children have exited,
   and port `30000` is free on IPv4 and IPv6. Check for another process using
   the same data root. If shutdown fails or remains uncertain, leave the host
   running, retain all evidence, and end the adoption attempt. Do not copy live
   databases as a verified backup or start a second Foundry process.
5. With writes stopped, create the complete independent recovery copy. Flush
   filesystem writes before any approved EBS snapshot; use the separately
   reviewed AWS procedure for root-volume consistency. Preserve earlier
   snapshots and backups. New snapshots do not replace the complete recovery
   inventory or authorize deletion of any existing recovery point.
6. Verify the copy's checksum inventory, required paths, both world IDs,
   versions, ownership metadata, configuration and external dependencies.
   Record capture time and the evidence that Foundry was stopped throughout.
   Restore it to the approved isolated target and inspect both worlds and their
   required assets before relying on the new lifecycle. Record restore-drill
   evidence separately from checksum verification.

The offline tests verify signal and failure handling. They cannot prove that
Foundry `14.366.0` flushes campaign writes on `SIGTERM`. Inspect shutdown logs
and verify the captured world's last saved changes in the isolated restore
during issue #10 before claiming that this stop procedure preserves a session.

If preservation or verification fails, do not adopt. Use the rollback rules
below to return to the recorded process configuration only after confirming
that no other Foundry process exists.

## 3. Install the reviewed files while Foundry is stopped

1. Render the completed manifest locally and review every generated file.
   Run the repository's offline checks. Transfer the reviewed result through
   the approved access path and verify its hashes before installing it.
2. Keep the original dedicated volume mounted at `/foundry`. Match its UUID in
   both the manifest and `/etc/fstab`; preserve its contents. Install the
   rendered mount entry through a reviewed edit to `/etc/fstab`, preserving
   unrelated entries. Never format the existing volume or replace its contents
   with a fresh directory tree.
3. Preserve the recorded `foundry` UID/GID. Make `/foundry`, the installed application,
   lifecycle code, manifest, and systemd configuration root-owned. Make the
   data root and its `Data`, `Config`, and `Backups` directories accessible to
   `foundry:foundry` as the host contract requires. Review unexpected owners and
   symlinks before changing metadata. Restrict any recursive ownership change
   to inventoried data paths and retain the original metadata for rollback.
4. Install `/etc/foundry/host.json`, the rendered `/etc/foundry/certbot.ini`,
   `/usr/local/lib/foundry/hostlib.py`, `/usr/local/sbin/foundry-host`, and the
   rendered `foundry.service`, `foundry-cert-renew.service`, and
   `foundry-cert-renew.timer` using the host contract's ownership and modes.
   Preserve the previous files separately; do not overwrite the rollback copy.
   Preserve and remove old `foundry.service` drop-ins from their active
   locations. Reconcile any inherited drop-ins before proceeding. The lifecycle
   rejects every effective drop-in, including one that appears harmless.
5. Edit the preserved installation's
   `/foundry/foundrydata/Config/options.json` to set `sslCert` to
   `/etc/foundry/tls/current/fullchain.pem` and `sslKey` to
   `/etc/foundry/tls/current/privkey.pem`. Set `port` to `30000` and `hostname` to
   `foundry.craigdevjohnson.com`. Preserve every unrelated option and the exact
   original file for rollback. The lifecycle does not repair this file at
   startup or supply undocumented certificate command-line overrides.
6. Review the existing Certbot lineage and authenticator against the host
   contract. Require the fixed `foundry.craigdevjohnson.com` lineage to use the
   `standalone` authenticator. Preserve and remove every configured lineage
   hook; the lifecycle rejects any nonempty hook. Require no installer plugin;
   an installer can reload another service even when hooks are disabled. Use the rendered
   `/etc/foundry/certbot.ini` so that global Certbot configuration does not add
   hooks. The command also disables directory hooks. Confirm that the approved
   HTTP-01 challenge can reach port `80` while Foundry is stopped. Leave
   certificate staging to `foundry-host`; it validates the material before
   switching `/etc/foundry/tls/current`.
7. Disable the inventoried competing launch and renewal paths after preserving
   their states. This includes the old Certbot timer and any confirmed cron,
   session or login launcher. Archive legacy Foundry start/stop scripts so that
   normal operations use the fixed commands. Do not assume the baseline found
   every launcher.
8. Reload systemd, enable the reviewed `foundry.service`, and enable and start
   `foundry-cert-renew.timer`. Do not start a legacy launcher to test the unit.
   Enablement alone is not evidence of a successful boot or valid TLS.

Render on the workstation, using the completed private manifest:

```sh
python3 host/render.py --manifest /private/path/host.json --output /private/path/rendered
```

The paths in this command are local destinations chosen for the adoption
record. Review the generated `/etc/fstab` fragment before integrating it; never
replace the whole host file with the fragment.

After installing and reviewing the files on the authorized target:

```sh
sudo systemctl daemon-reload
sudo systemctl enable foundry.service
sudo systemctl enable --now foundry-cert-renew.timer
```

Before the first start, inspect the effective service configuration:

```sh
sudo systemctl show foundry.service --no-pager --property=User,Group,KillMode,KillSignal,SendSIGKILL,Restart,FragmentPath,DropInPaths,TimeoutStartUSec,TimeoutStopUSec
```

Require `User=foundry`, `Group=foundry`, `KillMode=control-group`,
`KillSignal=15`, `SendSIGKILL=no`, and `Restart=no`. Require
`FragmentPath=/etc/systemd/system/foundry.service` and an empty `DropInPaths`.
Verify the rendered timeouts remain 600 seconds for startup and 90 seconds for
shutdown. If a setting differs, reconcile the installed unit before starting.

## 4. Prove the adopted lifecycle

Run the following as the approved host operator. Store command exit codes and
structured results with the adoption record. A successful `start` proves the
local service checks; complete the public and world checks below as well.

```sh
sudo /usr/local/sbin/foundry-host start
sudo /usr/local/sbin/foundry-host status
```

Require exit `0` and the action's expected `outcome` in the schema `1` JSON
result. An error returns exit `1`; a busy lifecycle lock returns exit `2`. Treat
both as unfinished operations. Resolve their reported state before retrying
or progressing to another interruption.

Allow the complete startup budget when testing: preflight waits up to 60
seconds for clock synchronization and up to 200 seconds for the certificate
lock, then allows up to 180 seconds for targeted Certbot renewal. The unit has
a 600-second startup timeout. After systemd completes startup, the wrapper
allows 60 seconds for HTTPS readiness. A readiness timeout can leave Foundry
running; inspect `status` before taking another action.

- Verify one intended Node process belongs to `foundry.service`, runs as the
  pinned `foundry:foundry` IDs, and owns every port `30000` listener. Check the
  process command line, pinned runtime/application, and data path. The readiness
  check verifies the live executable, exact arguments, all recorded process
  UIDs/GIDs, and a locally routed HTTPS response with normal TLS validation.
- Verify the expected `/foundry` mount and ownership after startup. Check the
  public hostname, trusted TLS chain, certificate name and dates, and Foundry's
  `/join` response. Host running state alone does not prove readiness.
- Open each existing world through Foundry and inspect required systems,
  modules, scenes and assets. Verify the saved configuration and local backups.
  Check that no version migration or upgrade occurred.
- Outside play and preparation, run `foundry-host stop` with the same absolute
  executable path and `sudo`. Verify successful unit exit, an empty service
  cgroup, and no remaining listener. Only then run `foundry-host start` again.
- Run `foundry-host restart` in the approved window and verify a new managed
  process becomes ready while the EC2 instance stays running. If stop fails,
  restart must report failure without launching a replacement.
- Run `foundry-host renew` and inspect the targeted Certbot result. Verify that
  the running Node PID and served certificate remain unchanged until the next
  deliberate start. Require `outcome=certificate-available`; this result means
  a valid lineage pair is available, and does not prove that Certbot issued a
  new certificate. Inspect the renewal timer's next run and logs. Exercise
  actual renewal with the approved Certbot test procedure on the isolated
  target, including the current authenticator's access requirements. After a
  deliberate start, compare the served certificate fingerprint with the staged
  pair and record the result. The readiness request does not compare those
  fingerprints.
- On the isolated target, test boot with a renewal-due certificate after a
  simulated period offline. Verify that startup checks the clock, mount and
  listener, attempts targeted renewal, stages valid certificate material, and
  confirms that `foundry` can read both staged files before starting Node.
  Verify that it reaches HTTPS readiness. Test renewal failure with a still-valid certificate,
  then expired, wrong-name, mismatched-key and malformed certificate material.
  With no usable certificate, Foundry must remain unstarted.
- On the isolated target, test a missing or wrong mount, unmanaged listener,
  and process that refuses graceful stop. Verify that failure never creates a
  data tree on root, kills an unrelated process, starts a duplicate, or reports
  a safe stop.

After the backup workflow and management integration pass their own acceptance
checks, execute the authorized End session/Start session cycle from issue #10.
Verify a complete durable backup before EC2 stop, actual EC2 stopped/running
states, mount identity after boot, renewal handling, one managed listener,
HTTPS readiness, and both usable worlds. Keep the latest 10 successful session
backups and all unreleased pre-upgrade copies under the agreed recovery policy.
Do not use a direct EC2 stop as a substitute for End session.

## Roll back without introducing a second writer

Keep the pre-adoption copy, original metadata, application/runtime artifacts,
service files, launcher configuration and certificate state until Craig
explicitly releases them. Record which files changed during adoption and which
world data changed during live validation.

1. Confirm the interruption window remains authorized. Stop the adopted service
   with `sudo /usr/local/sbin/foundry-host stop`. If stop fails or the process
   state is uncertain, do not restore data, unmount storage, change ownership,
   start the old process, or stop EC2. Leave the host running and reconcile the
   exact process and cgroup first. This failure requires an operator decision;
   rollback is not permission to force termination.
2. Once no Foundry writer or listener remains, preserve a separate consistent
   copy of any data changed after adoption. Do not silently overwrite new
   campaign work with the pre-adoption copy.
3. Disable the new service and renewal timer. Restore the original systemd
   files, drop-ins, mount configuration, scripts, Certbot configuration and
   enablement states from the inventory. Reload systemd. Retain the existing
   data volume, Elastic IP association, snapshots and all backup sets.
4. Restore ownership, modes, ACLs and other changed metadata from the saved
   per-path record. Restore the exact original runtime and application artifacts
   only if they changed or verification finds damage. If data restoration is
   needed, choose the recovery point explicitly and restore into an isolated
   path first. Verify both worlds before replacing a stopped data tree.
5. Restore the original Foundry TLS configuration. Verify that the certificate
   is still valid for the public name and matches its private key. If the saved
   certificate has expired, obtain an approved valid replacement; do not force
   the old certificate into service or disable TLS verification.
6. Recheck that port `30000` is free, no Foundry process uses the data root, and
   no new or restored launcher can race the selected launch. Reproduce only the
   captured, reviewed legacy command with its original user, working directory
   and environment. Do not start the previously enabled but inactive legacy
   `foundry.service` as well as the captured session process.
7. Verify one intended serving process, the original versions, public HTTPS,
   both worlds, assets, configuration and backups. Record the rollback outcome
   and any unresolved failure. Leave the management workflows disabled until
   the single-supervisor adoption is repaired and revalidated.

Do not delete newly created copies, temporary recovery resources or the old
installation as part of rollback unless the separately approved cleanup plan
names them. Record actual public/runtime evidence separately from merged code
and passing offline checks.
