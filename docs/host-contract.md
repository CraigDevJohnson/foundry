# Host setup and lifecycle contract

Contract version 1 implements the offline slice in [issue #5](https://github.com/CraigDevJohnson/foundry/issues/5).
[Adoption and rollback](host-adoption.md) is a separate, human-authorized live
procedure under issue #10. No installer, licensed application, private key,
host credentials, or deployable manifest is included here.

## Reconstruction inputs

[host/host.example.json](../host/host.example.json) is the manifest schema and
incomplete baseline. `null` values deliberately prevent rendering. The existing
baseline establishes Node `24.18.1`, Foundry package `14.366.0`, Amazon Linux 2023,
ARM64, a dedicated ext4 mount, and two worlds. It does not establish the remaining
pins. The renderer rejects upgrades, missing pins, malformed values, and an
existing output directory before writing an installation tree.

| Manifest input | Contract |
| --- | --- |
| `schema` | Integer `1` |
| `setup_revision` | Full reviewed Git commit of this repository used for the render |
| `os.ami_id` | Exact retained ARM64 AMI in `us-west-2`, never a `latest` parameter |
| `os.architecture` | `aarch64` |
| `os.system_release` | Exact `/etc/system-release` string, including the AL2023 repository release |
| `mount_uuid` | Existing dedicated ext4 filesystem UUID, never an NVMe device name |
| `uid`, `gid` | Existing numeric `foundry:foundry` identity, both nonzero |
| `node_version`, `node_binary_sha256`, `node_archive_sha256` | `24.18.1`, installed `/usr/bin/node` digest, and retained matching ARM64 distribution archive digest |
| `foundry_version`, `foundry_main_sha256`, `foundry_archive_sha256` | `14.366.0`, installed `main.js` digest, and privately retained licensed distribution archive digest |
| `worlds` | Both existing world directory names, no invented campaign IDs |
| `rpms` | Exact `rpm -q --qf '%{NEVRA}' NAME` output for Python, systemd, OpenSSL, Certbot, iproute, util-linux, curl, and CA certificates |

A reconstruction set retains the actual matching archives and AMI or a tested
replacement image, the complete manifest, rendered files, and configuration.
Hashes alone cannot reconstruct an unavailable artifact. Record archive retrieval
locations in the private recovery inventory from issue #6. Verify SHA-256 before
extraction. Preserve a package-file inventory to detect drift beyond `main.js`.
The runtime checks the OS release, architecture, prerequisite RPMs, Node version
and binary digest, Foundry version and entry-point digest. The full licensed
package and AMI provenance are adoption and restore checks.

Render from a clean Git checkout whose `HEAD` equals `setup_revision`. The
renderer checks Git metadata, tracked changes, and untracked files before
creating output. It also rejects ignored files that would be copied as render
inputs. Keep the private manifest and staging output outside the checkout.
Retain the checkout with its Git metadata in the recovery inventory; an archive
without Git metadata cannot establish the source revision for rendering.

The runtime uses Python's standard library and supports Python 3.9 or newer.
There is no package-manager or application update in a lifecycle command.
Exact package pins can prevent startup after an unreviewed OS update. Refresh
pins only through a reviewed maintenance change with a pre-upgrade backup.

## Filesystem and ownership

| Path | Owner and permissions | Consumer |
| --- | --- | --- |
| `/foundry` | `root:root`, `0755`, exact ext4 UUID mounted read/write | Every start and readiness check |
| `/foundry/foundryvtt` | `root:root`, directories `0755`, files not writable by `foundry` | Preserved Foundry distribution |
| `/usr/bin/node` and its resolved target | Root-owned, executable, not writable by `foundry` | Pinned runtime |
| `/foundry/foundrydata` | `foundry:foundry`, directories `2770`, private files `0660` or stricter as appropriate | Shared installation user data |
| `foundrydata/Data` | Same identity; preserve both `Data/worlds/<id>` directories and all assets, systems, modules | Complete session recovery |
| `foundrydata/Config` | Same identity; preserve all configuration including secrets | Complete session recovery |
| `foundrydata/Backups` | Same identity; preserve existing package backups and manifests | Existing recovery history, never pruned by this work |
| `/etc/foundry/host.json` | `root:root`, `0600` | Runtime manifest and recovery metadata |
| `/etc/foundry/certbot.ini` | `root:root`, `0644`, no lifecycle hooks | Fixed Certbot invocation |
| `/etc/foundry/tls/pair-*` | `root:foundry`, directory `0750`, certificate and key `0640` | Immutable certificate pairs |
| `/etc/foundry/tls/current` | Root-owned symlink to one complete pair | `Config/options.json` TLS paths |
| `/etc/letsencrypt` | Preserve Certbot permissions, lineage, account, renewal config and archive links | Private host reconstruction; restrict recovery access |
| `/usr/local/lib/foundry/hostlib.py`, `/usr/local/sbin/foundry-host` | Root-owned, `0644` and `0755` | Fixed host commands |
| `/etc/systemd/system/foundry*.service`, `foundry-cert-renew.timer` | `root:root`, `0644` | Sole service supervisor and renewal schedule |
| `/run/lock/foundry-lifecycle.lock` | Root-owned `0600`, advisory exclusive lock | Lifecycle and future backup commands |
| `/run/lock/foundry-certificate.lock` | Root-owned `0600`, advisory exclusive lock | Certificate fetch and activation |

Runtime checks reject missing mounts, wrong filesystem UUID/type, read-only
mounts, unexpected root-directory ownership, symlinked required directories,
and missing worlds. They include the `Data/worlds` container and require each
checked data directory to resolve beneath the data root. The adoption audit checks ownership recursively, required
files and symlink targets, external asset locations, and available space. Runtime
checks do not replace that coverage audit. Never create replacement data
under the root filesystem when `/foundry` is absent.

The rendered `etc/fstab.foundry` is a fragment for review, not a replacement
`/etc/fstab`. `nofail` allows Systems Manager access if the disk is missing.
`RequiresMountsFor=/foundry` and the explicit UUID check block Foundry in that
case. Never format or replace the retained filesystem during adoption.

`options.json` retains all existing settings and the selected world, with these
reviewed values: `port: 30000`, `hostname: foundry.craigdevjohnson.com`,
`sslCert: /etc/foundry/tls/current/fullchain.pem`, and
`sslKey: /etc/foundry/tls/current/privkey.pem`. TLS paths are configuration fields.
The documented CLI supplies only `--dataPath` and `--port`.

## Fixed commands for Automation

Commands run as root through the configured host's SSM command document. No
command accepts a path, shell fragment, PID, hostname, world, or EC2 target.
The controller in issue #7 must pin the command document and setup revision.

| Command | Success |
| --- | --- |
| `/usr/local/sbin/foundry-host start` | Starts an inactive, verified-stopped unit; waits for HTTPS readiness. An already-ready unit returns success. |
| `/usr/local/sbin/foundry-host stop` | Requests systemd shutdown and proves the stopped conditions below. Already-stopped is idempotent. |
| `/usr/local/sbin/foundry-host restart` | Completes verified stop before start. A failed stop never reaches start. |
| `/usr/local/sbin/foundry-host status` | Reports `ready` or verified `stopped`; other states are errors with a diagnostic. |
| `/usr/local/sbin/foundry-host renew` | Fetches and validates the fixed certificate lineage without restarting Foundry or activating new files. |

Success exits `0` and writes one JSON object to stdout. For example:

```json
{"schema": 1, "action": "stop", "outcome": "stopped"}
```

Start and restart return `ready`. Renewal returns `certificate-available`, which
also covers a still-valid certificate when renewal was not due. A failure exits
`1` with `outcome: error` and a `message`. Lock contention exits `2`, also with
`outcome: error`. Treat missing/malformed output, command timeout, failed SSM
execution, or any nonzero exit as unknown or failed, never as stopped or ready.
Diagnostics from boot renewal also go to stderr and the journal.

Every verb supports `--help` without reading the manifest or touching the host.

The internal `preflight` verb belongs to `ExecStartPre` and returns `prepared`.
Do not expose it in the Automation command allowlist. It validates the mount,
versions, ownership, clock synchronization, absence of a listener or surviving
service process, and certificates before Node starts. It does not acquire the
lifecycle lock because the calling wrapper may hold that lock while waiting for
systemd. systemd serializes unit jobs; preflight and renewal share the certificate
lock. Preflight waits up to 200 seconds for an in-progress renewal, then fails
safely if the lock remains held. Clock synchronization has a separate 60-second
wait. The unit startup timeout is 600 seconds; Automation needs a command
timeout of at least 720 seconds to include startup and readiness checks. Only the trusted operator can invoke raw systemd or legacy launchers.

### Shutdown and readiness evidence

`foundry.service` runs one foreground Node process as `foundry:foundry`.
`KillMode=control-group`, `KillSignal=SIGTERM`, `TimeoutStopSec=90`, and
`SendSIGKILL=no` allow bounded graceful shutdown without an automatic forced kill.
`Restart=no` prevents a crash or failed stop from silently starting a writer.
No `nohup`, cron launcher, second supervisor, or automatic restart is part of
this contract.

A successful stop requires all of the following at observation time:

- `ActiveState=inactive`, `SubState=dead`, and `Result=success`.
- `MainPID=0` and `ControlPID=0`.
- No processes in the fixed service cgroup or descendant cgroups.
- No TCP listener on port 30000, including IPv6 and listeners with unknown owners.
- No unmanaged Foundry or data-path process in the host process inventory,
  including one that has not opened or has already closed its listener.

A failed unit result blocks start until the operator inspects the journal,
processes, and data and deliberately reconciles it. Normal commands never use
`reset-failed`, adopt an unmanaged PID, send `killall`, or escalate signals.
Shutdown semantics and persisted game data in the pinned licensed application
must still be demonstrated during issue #10. Offline tests do not prove a
successful session backup or database flush.

Before requesting startup, the wrapper verifies the effective systemd execution
and shutdown contract, including the mount dependency, working directory,
timeouts, environment, privilege settings, and absence of drop-ins or extra
commands. It reads structured command properties through `busctl` to verify
exact argument boundaries and the privileged preflight flag. Drift or an
unloaded unit blocks startup before systemd can execute its commands.

Readiness repeats this contract check. It checks the process executable, arguments, and all user
and group IDs against the fixed command and manifest. It also requires the
correct writable mount, an active successful unit, and
exactly its main PID in the cgroup, port-30000 listener inventory, and Foundry
process inventory. The process scan checks executable, arguments, and working
directory across `/proc`; an unreadable live process or reused PID makes the
result uncertain and blocks the operation. A local
HTTPS request verifies the public hostname and trusted certificate, bypasses
proxies, and accepts HTTP 200, 302, or 303 without following a redirect. This
proves application reachability, not that either world can be played. Issue #10
checks both worlds and the external endpoint separately. Start waits up to 60
seconds after systemd startup for readiness. Timeout leaves the observed process
alone and reports uncertainty.

### Backup integration

Issue #6 must hold `foundry-lifecycle.lock` across verified shutdown, coverage
capture, and any restart. Its installed root command can import `Host` and
`locked`, then call `Host.stop()` within one lock scope. It must not call the
locking CLI while already holding the lock. A separate `stop` command followed
by an unlocked archive is insufficient because another command could start
Foundry between those steps.

The backup command must recheck the mount and stopped evidence immediately
before reading Data and Config and preserve the manifest/setup/certificate
reconstruction dependencies and inventoried external assets. Existing `Backups`
and snapshots remain preserved independently. Logs, cache and transient files
need an explicit coverage decision in issue #6. No backup or host shutdown is
implemented here. An error must leave EC2 running; it does not necessarily mean
Foundry is still serving. Issue #7 owns admission across complete workflows and
reconciliation after disconnected or timed-out callers.

## Certificates after off-hours shutdown

The fixed standalone Certbot lineage is `foundry.craigdevjohnson.com`. The
baseline's timer is disabled and old scripts/hooks need inventory before
replacement. The new timer runs twice daily, catches missed calendar events
with `Persistent=true`, and adds up to 15 minutes of jitter. It fetches only;
there is no restart or deployment hook during play.

Every service start synchronously attempts targeted renewal after network and
clock readiness. Saved lifecycle hooks and installer plugins are rejected, directory hooks are
disabled, and the command uses a dedicated empty configuration file and
`--no-random-sleep-on-renew`. A valid lineage is copied into a new private pair,
checked again, and exposed by one atomic symlink replacement before Node starts.
Old pairs remain available for rollback. They can be pruned only by a later
reviewed maintenance operation.

Validation checks chain trust, server purpose, hostname, validity dates, at
least 24 hours of remaining lifetime, key parsing, and certificate/key match.
The service identity must be able to read both activated files. If the Certbot
attempt fails after its configuration passes validation, startup can use the
existing active pair only if it independently passes all checks. Invalid lineage
configuration, legacy hooks, installer plugins, and invalid renewed certificate
material always block startup. Invalid, expired, not-yet-valid, or missing TLS with failed renewal
blocks startup. A stopped host cannot renew; its next Start session performs
this check. A still-running session continues using the certificate loaded at
startup until the next confirmed restart. Persistent runs lasting through
certificate expiry require operator attention; daily renewal alone does not
reload Foundry. Monitor renewal journal failures through issue #7.

HTTP-01 requires public inbound TCP 80, correct DNS, and outbound ACME access.
Foundry uses HTTPS on 30000 and does not need to stop for the challenge itself.
Those live prerequisites, external TLS, and a staging dry run belong to issue
#10. See the [upstream source notes](host-upstream-notes.md) for verified tool
semantics and the limits of the Foundry shutdown documentation.

## Offline verification

Run `python3 -m unittest discover -s tests -v` with Python 3.9 or newer and
OpenSSL on `PATH`. On macOS, use Homebrew OpenSSL for certificate fixtures.
CI runs this suite and validates the systemd unit syntax in a disposable Linux
environment. Tests substitute
host boundaries, never contact the existing host, and exercise missing mounts,
unmanaged listeners, failed shutdown, certificate errors, and rendering.
Linux unit parsing is distinct from booting Amazon Linux with the licensed
Foundry package. Live acceptance remains in the adoption guide.

Validation on 2026-09-15 passed all 47 tests on the development workstation and
in an ARM64 Amazon Linux 2023 container with Python 3.9.25. The same container's
systemd 252.23 parsed all three templates successfully. The test image was
`amazonlinux@sha256:065856aabb1ddab0441f4024dfbba116cc760286858a53ec74e6a1218af979ee`.
It is a disposable test environment, not the adoption AMI pin. Inert executables
satisfied unit-parser path checks; no systemd service was started. `actionlint`,
`zizmor --offline --strict-collection --persona pedantic .github`, and the
Gitleaks working-file scan also passed. These results describe local
verification. GitHub Actions results are recorded separately on the pull request.
