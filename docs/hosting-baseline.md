# Existing hosting baseline

> **Historical.** This records the hand-built host in the management account
> as observed on 2026-09-08. Foundry moved to a new host in the workloads
> account on 2026-09-29, and the old host was stopped the same day. For the
> current host and how to operate it, see [operations](operations.md).

Observed on 2026-09-08 through read-only discovery. This is an inventory of the
existing installation, not a target architecture or an implementation spec.
Recheck these observations before making operational changes.

## Hosted application and infrastructure

| Area | Observed state | Evidence |
| --- | --- | --- |
| Public application | `https://foundry.craigdevjohnson.com:30000/` responds with a redirect to `/join`; the public page reports Version 14 Build 366 | HTTPS response and public page |
| Compute | One matching running EC2 instance, `t4g.small`, ARM64, in `us-west-2a` | EC2 lookup scoped to the public endpoint's resolved address |
| Public address | An associated Elastic IP matches the public hostname's DNS address | EC2 address lookup scoped to the instance and DNS comparison |
| Operating system | Amazon Linux 2023 | Systems Manager instance information |
| Remote management | Systems Manager agent reports Online | Systems Manager instance information |
| Storage | Two attached encrypted gp3 EBS volumes: 10 GiB root and 20 GiB additional volume | EC2 instance and volume descriptions |
| Termination behavior | Root volume has `DeleteOnTermination=true`; additional volume has `DeleteOnTermination=false` | EC2 instance block-device mappings |
| Volume snapshots | Two completed snapshots returned for the additional volume; newest dated 2026-06-02. No snapshots returned for the current root volume | EC2 snapshot lookup scoped to those two volume IDs |
| TLS | Served certificate valid from 2026-08-19 through 2026-11-17 | Public TLS certificate |
| SSH access | The configured `foundry` SSH alias timed out from the inspecting machine | Noninteractive SSH connection attempt |

The SSH timeout does not indicate that the application is down: the public
application responded. Subsequent read-only host inventory succeeded through
Systems Manager Run Command. No service, backup, renewal, or configuration
changes were made during discovery.

The existing Elastic IP provides address stability across ordinary stop/start
while the association is retained. Application startup and health after boot
have not been tested. The volume termination flags concern terminating the
instance, not stopping it. See
[AWS stop/start behavior](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/how-ec2-instance-stop-start-works.html).

The volume snapshot inventory does not establish an application backup schedule,
backup consistency, coverage of all campaign data, or successful restoration.

## Host inventory through Systems Manager

| Area | Observed state |
| --- | --- |
| Serving process | `/usr/bin/node` v24.18.1 runs `/foundry/foundryvtt/main.js` as `ec2-user`, using `/foundry/foundrydata`; it owns port 30000 |
| Process supervision | The serving process belongs to `session-32.scope`, outside `foundry.service` |
| Configured service | `/etc/systemd/system/foundry.service` is enabled but inactive/dead; configured user/group `foundry`, working directory `/foundry`, restart policy `on-failure` |
| Application package | Foundry package metadata reports `14.366.0` |
| Filesystems | Root is XFS; `/foundry` is a dedicated ext4 mount |
| Free space | Approximately 1.96 GiB available on root and 11.36 GiB on `/foundry` |
| User data | `/foundry/foundrydata/Data`, approximately 2.46 GiB; two world directories |
| Configuration | `/foundry/foundrydata/Config`, approximately 5.34 MiB; a default world is configured |
| Local package backups | `/foundry/foundrydata/Backups`, approximately 4.52 GiB; 78 `.bak` files and 80 `.json` files; newest file modification time 2026-08-15 16:13:36 UTC |
| Ownership | Application directory owned by `foundry:foundry`, mode `2775`; data root and `Data`, `Config`, `Backups` directories owned by `foundry:foundry`, mode `2770` |
| Existing lifecycle scripts | `/foundry/start_foundry.sh` contains `nohup`/`node`; `/foundry/stop_foundry.sh` contains `node`/`kill`; neither was executed |
| Renewal timer | `certbot-renew.timer` is disabled and inactive, with no reported previous or next trigger; `certbot-renew.service` exists and is inactive |
| Renewal script | `/foundry/renew_cert.sh` exists and invokes Certbot with `--no-random-sleep-on-renew`; it was not executed |

No relevant backup or renewal entries were found in the narrowly inspected cron
files or backup-related system timer names. This does not prove that no other
automation exists. Local backup archives and manifests were not opened, so
their modification times and counts do not establish completeness or
restorability. They reside on the same mounted filesystem as the live data.

The running process and configured service have different owners and supervision
paths. A single, verified service lifecycle is a prerequisite for reliable
session controls; automatic startup after EC2 stop/start is still untested.
Upgrade procedures and a working renewal schedule also remain unverified.

The three read-only SSM diagnostic commands returned Success with exit code 0:
`a0d8b0a8-061a-4c5b-897c-e79b5f097510`,
`c2ead4aa-f5a7-4999-8503-d4afd357fd06`, and
`b0147cf4-c7ed-41b0-a6ab-fc4e292082ab`.

## Illustrative hosting cost

Public list rates checked on 2026-09-08 for Oregon are $0.0168 per running hour
for Linux On-Demand `t4g.small`, $0.08 per GB-month for baseline gp3 storage, and
$0.05 per GB-month of standard EBS snapshot data. See the
[AWS Oregon EC2 Price List](https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/us-west-2/index.csv).
An allocated public IPv4 costs $0.005 per hour, including while idle. See
[Amazon VPC pricing](https://aws.amazon.com/vpc/pricing/).

Assuming a 730-hour month, 30 GiB of gp3 at baseline performance, and retention
of the current Elastic IP for the whole month:

| Host running time | Compute, volume storage, and public IPv4 |
| --- | --- |
| Stopped all month | $6.05 |
| 40 hours in the month | $6.72 |
| Running all month | $18.31 |

These are examples, not the account's actual bill or an agreed budget. They
exclude snapshots, additional storage performance, transfer, DNS/domain costs,
logging, management-plane resources, possible surplus CPU credits, tax, and
discounts. Snapshot charges depend on actual retained data, not simply volume
capacity multiplied by snapshot count.

Stopping the host reduces compute cost while retained volumes, the Elastic IP,
and snapshots continue to incur charges. New architecture proposals should
compare their incremental cost with this small existing footprint. Monthly play
and preparation hours are unknown; the agreed backup retention policy is
recorded in the README.

## Recovery constraints from upstream documentation

Foundry's built-in backups cover worlds, systems, and modules, including assets
inside those package directories. Assets elsewhere and the separate `Config`
directory are outside that coverage. Built-in backups alone therefore do not
establish full installation recovery. See
[Foundry Backups and Snapshots](https://foundryvtt.com/article/backups/).

Foundry's documented Linux procedure stops the application, verifies that its
process has exited, and archives `Data` and `Config`. Its restore procedure also
requires the application to be stopped. See
[Foundry user-data backup and restore](https://foundryvtt.com/article/user-data-backup/).

EBS snapshots omit data still cached by the application or operating system.
AWS recommends pausing writes or unmounting a volume before snapshotting it, and
stopping the instance for a root-volume snapshot. See
[AWS snapshot consistency guidance](https://docs.aws.amazon.com/ebs/latest/userguide/ebs-creating-snapshot.html).

The recovery design must account for these constraints. The agreed retention
and restore-drill policy is recorded in
[the recovery decision](adr/0002-session-based-recovery.md). Coverage verification
and backup automation remain implementation work; these references do not
select an implementation.

## Existing portfolio management code

The portfolio repository's
[GitHub main commit `02ca989a`](https://github.com/CraigDevJohnson/portfolio/tree/02ca989a92ad862c8cc27d40cfe7b03c7b241996)
contains an authenticated EC2 management portal. This was source inspection;
deployment and configuration of that portal were not verified.

- `internal/app/server.go` registers `/mgmt` and instance action, metric, and log
  routes.
- `internal/portal/auth.go` implements Cognito login and an operator email
  allowlist.
- `internal/portal/mgmt.go` implements EC2 lifecycle actions. Its restart action
  requests EC2 stop followed by start; this is distinct from restarting the
  Foundry VTT application.
- `internal/portal/metrics.go` reads CloudWatch CPU metrics and instance logs.
- `internal/portal/preview.go` provides a separate mock preview without AWS or
  Cognito clients.

The portfolio uses Go, Templ, HTMX, and Tailwind. Its Go handlers call AWS
server-side, and the authenticated operator email is available in their request
context. These are existing integration points; a separate browser login or
browser-facing Foundry backend is not required by the agreed interface boundary.

The current portfolio runtime IAM policy and its execution permissions boundary
do not include `lambda:InvokeFunction`. A Lambda-based backend would therefore
require narrowly scoped changes in both places. Its generic EC2 routes also
accept an instance ID and invoke EC2 directly: retaining direct lifecycle
permissions for the Foundry instance would bypass the new backend's workflows.
These are integration constraints, not changes already made.

That source snapshot has no Foundry VTT service, world, package, or backup
operations. The agreed direction is to retain the portfolio interface and have
it call a backend owned here; see
[the responsibility decision](adr/0001-portfolio-and-foundry-responsibilities.md).
The source inspection does not establish that this integration already exists.
