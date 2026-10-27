# Operating the Foundry host

How to run the Foundry host in the workloads account, where it moved on
2026-09-29. The OpenTofu root that builds it, and how to rebuild it, is in
[infra](../infra/README.md).

Reads use the `workloads-readonly` profile. Anything that changes AWS or runs
on the host (Run Command, Session Manager) uses `workloads-admin`, and an agent
runs it only as a change Craig has approved. The commands assume the profile's
Region is us-west-2; otherwise add `--region us-west-2`.

## Host

| Item | Value |
| --- | --- |
| Instance | `i-05dfb9abbc1caac32`, t4g.small, us-west-2a, workloads account 793680745829 |
| Address | Elastic IP `52.27.248.145` (`eipalloc-03508fd8e37c5f068`) |
| Data volume | `vol-01ef214b56c69e2ad`, ext4 label `foundry-data`, mounted on `/foundry` by `/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_vol01ef214b56c69e2ad` |
| Root volume | `vol-0956156f289881032` |
| Security group | `sg-0aa47e89292e89781`: 80/tcp and 30000/tcp from anywhere, no SSH |
| Snapshots | DLM `policy-0fb1290b3a1a8478c` on both volumes: daily at 10:00 UTC keeping 7, weekly on Sunday at 11:00 UTC keeping 4 |
| Service | `foundry.service` runs `/usr/bin/node-24 /foundry/foundryvtt/main.js --dataPath=/foundry/foundrydata --port=30000` as `foundry`, with `Restart=on-failure`. `Config/options.json` launches the `blood-lords` world at start |

A rebuild changes the instance and root volume IDs; `tofu -chdir=infra output`
prints the current ones (`host_instance_id` and `root_volume_id`).

There is no key pair. Open a shell with Session Manager (needs the Session
Manager plugin), or run commands with Run Command:

```sh
aws ssm start-session --profile workloads-admin --target i-05dfb9abbc1caac32

aws ssm send-command --profile workloads-admin \
  --instance-ids i-05dfb9abbc1caac32 --document-name AWS-RunShellScript \
  --parameters '{"commands":["systemctl is-active foundry.service"]}' \
  --query Command.CommandId --output text
aws ssm get-command-invocation --profile workloads-admin \
  --instance-id i-05dfb9abbc1caac32 --command-id <command id> \
  --query '[Status,StandardOutputContent]' --output text
```

## Start and stop

These are the Start session and End session actions in
[CONTEXT.md](../CONTEXT.md). A stop takes no backup of its own: the latest
recovery point is the last scheduled snapshot, and the next one holds the
finished session (Craig's choice, 2026-10-16, [#7](https://github.com/CraigDevJohnson/foundry/issues/7)).
The Lobby's site can start and stop the host too, through the controller
([below](#the-lobbys-controller)), but no page offers it yet: `/foundry` shows
only the status.

The Taskfile's host tasks find the host by its `Name=foundry` and
`project=foundry` tags, so they keep working after a rebuild. Locally they use
the AWS profile in the environment:

```sh
AWS_PROFILE=workloads-readonly task host:status   # instance state and whether Foundry answers
AWS_PROFILE=workloads-admin task host:start       # start, then wait up to 5 minutes for Foundry
AWS_PROFILE=workloads-admin task host:stop        # shut Foundry down, then stop the instance
```

`host:stop` first runs `/foundry/stop_foundry.sh` through Run Command, using
the parameterless `foundry-shutdown` document
(`infra/templates/foundry-shutdown.sh`), then `systemctl stop foundry.service`, and
checks for 90 seconds that nothing listens on port 30000 and no live process
runs in `/foundry`, runs a `foundry` or `foundryvtt` executable, or names
`foundryvtt`, `/foundry/` or `--dataPath` (which also catches relative launches
such as `node main.js`). It then flushes the data volume with `sync -f`, which
reports writeback errors. If the script is missing, the service stop or the
flush fails, a process can't be inspected, or Foundry is still running, it
leaves the host
running and exits non-zero. A failing script alone doesn't stop it, since the
service stop and the process check decide. Stopping disconnects everyone, so
check that nobody is playing first. `host:stop` does nothing if the host is
already stopped, and only waits if it is already stopping; on a host that is
still starting it waits for it to boot, then shuts down as usual.
`host:start` on a running host only waits for Foundry. `foundry.service` is enabled, so Foundry starts
with the host.

The **Host control** workflow in GitHub Actions runs the same tasks. Start it
from the Actions tab, pick `status`, `start` or `stop`, and for `stop` tick
**nobody playing**. It runs in the `foundry-host` GitHub Environment and
assumes `foundry-host-control-ci` through the workloads GitHub OIDC provider.
`infra/bootstrap/roles.tf` makes the role and `infra/ci.tf` sets what it may
do: only describe instances, start and stop this instance, and run the
`foundry-shutdown` document on it, never arbitrary commands. Setup, once the role is applied:

1. Create the `foundry-host` environment under the repository's
   Settings > Environments. Set **Deployment branches** to `main` only: the
   role's trust policy pins the environment, not the branch, so this rule is
   what keeps a workflow from another branch from getting the role. No
   required reviewers: runs start without an approval step (Craig's call,
   2026-10-09).
2. Add the variable `AWS_HOST_CONTROL_ROLE_ARN` with the value of
   `tofu -chdir=infra/bootstrap output -raw host_control_role_arn`.

## DNS

Cloudflare holds `foundry.craigdevjohnson.com` as an A record for
`52.27.248.145`, proxy status **DNS only**, TTL Auto. It is set by hand
(aws-setup decision D19); nothing in this repo manages it. Keep it DNS only:
players connect straight to port 30000, which Cloudflare's proxy doesn't carry.

The Elastic IP has `prevent_destroy` and survives a rebuild, so the record
changes only if the address does. To change it:

1. Check that the new address serves Foundry. This must print `200`:
   `curl -sS -o /dev/null -w '%{http_code}\n' --resolve foundry.craigdevjohnson.com:30000:<address> https://foundry.craigdevjohnson.com:30000/join`
2. In the Cloudflare dashboard for `craigdevjohnson.com`, open DNS > Records,
   edit `foundry`, set the IPv4 address, leave the proxy status on DNS only,
   and save.
3. `dig +short foundry.craigdevjohnson.com @1.1.1.1` returns the new address.
4. On the host, `sudo certbot renew --dry-run` succeeds, which shows that the
   renewal challenge on port 80 reaches it.

## Certificate renewal

Certbot keeps the Let's Encrypt certificate in `/etc/letsencrypt` on the root
volume. It uses the standalone authenticator, which answers the challenge
itself on port 80. The certificate was renewed on 2026-10-27 and expires on
2027-01-25; the next renewal is due from about 2026-12-26.

- `certbot-renew.timer` must be enabled. On the old host it was found
  disabled.
- The timer runs daily at 10:00 UTC with up to 15 minutes of random delay, set
  in `/etc/systemd/system/certbot-renew.timer.d/override.conf`. Certbot renews
  only when the certificate is close to expiry, by default about 30 days
  before.
- The host may be off at 10:00 UTC between sessions, so user data's override
  also sets `Persistent=true`: a run missed while the host was off happens
  within 15 minutes of the next start. A renewal then restarts Foundry
  (below), so anyone who joined in those minutes is briefly disconnected.
- User data runs only at first boot, and OpenTofu ignores later changes to it.
  The host above was built on 2026-09-29, before `Persistent=true` was added,
  so its override lacks the line. The certbot package's own timer unit sets
  `Persistent=true`, so `systemctl show certbot-renew.timer -p Persistent`
  printed `Persistent=yes` on 2026-10-27, and the run missed that morning was
  scheduled within 15 minutes of the start. If it ever prints `Persistent=no`,
  add `Persistent=true` under `[Timer]` in the override, then run
  `sudo systemctl daemon-reload` and `sudo systemctl restart
  certbot-renew.timer`. The restart records when the timer started; without
  that record the next boot has nothing to catch up from.
- After a renewal, the deploy hook `/usr/local/sbin/foundry-cert-deploy`,
  linked from `/etc/letsencrypt/renewal-hooks/deploy/foundry.sh`, copies
  `fullchain.pem` and `privkey.pem` to `/foundry/foundrydata/Config/foundry.pem`
  and `foundry.key`, then restarts Foundry if it is running
  (`systemctl try-restart foundry.service`). The restart briefly disconnects
  anyone connected. `foundry.sh` must be the only file in that hook directory.

Check the timer and the last runs through Session Manager, or with Run Command
as above:

```sh
systemctl is-enabled certbot-renew.timer                # enabled
systemctl show certbot-renew.timer -p Persistent        # Persistent=yes
systemctl list-timers certbot-renew.timer --no-pager    # shows the next run
journalctl -u certbot-renew.service -n 20 --no-pager    # recent runs
```

```sh
aws ssm send-command --profile workloads-admin \
  --instance-ids i-05dfb9abbc1caac32 --document-name AWS-RunShellScript \
  --parameters '{"commands":["systemctl is-enabled certbot-renew.timer","systemctl show certbot-renew.timer -p Persistent","systemctl list-timers certbot-renew.timer --no-pager","journalctl -u certbot-renew.service -n 20 --no-pager"]}' \
  --query Command.CommandId --output text
```

From anywhere, this prints the served certificate's expiry. The timer only
runs while the host is up, so the date changes at the first run, or catch-up
at boot, once the certificate is within 30 days of expiry with the host
running. If the date is still within 30 days after such a run, the renewal
failed: read the journal.

```sh
openssl s_client -connect foundry.craigdevjohnson.com:30000 \
  -servername foundry.craigdevjohnson.com </dev/null 2>/dev/null |
  openssl x509 -noout -enddate
```

To renew without waiting for the timer, start the timer's service on the host
while nobody is playing: `sudo systemctl start certbot-renew.service`. Certbot
renews only when the certificate is due, and the deploy hook then restarts
Foundry.

`scripts/live-checks.py` does the whole check from Craig's computer in one
sitting (first run on 2026-10-27, recorded on
[#25](https://github.com/CraigDevJohnson/foundry/issues/25)): it
starts the host with Host control, reads the timer and the hook through Run
Command with `workloads-admin`, adds `Persistent=true` and relinks the hook
if they are missing, renews now, checks `/join` and the served certificate
from outside, waits for the browser checks, ends the session with Host
control, and writes a record to post on the issue. Every command it runs is
printed first; `--no-renew`, `--keep-running` and `--no-browser-pause` skip
the matching steps.

## Snapshot before an upgrade

DLM snapshots both volumes daily and weekly. Before upgrading Foundry or the
host, also take a manual snapshot of both volumes:

```sh
aws ec2 create-snapshots --profile workloads-admin \
  --instance-specification InstanceId=i-05dfb9abbc1caac32,ExcludeBootVolume=false \
  --copy-tags-from-source volume --description "foundry pre-upgrade"
```

For a clean copy, first run `sudo systemctl stop foundry.service && sync` on
the host, and start Foundry again once the command returns: a snapshot holds
the data as of the call. DLM doesn't expire manual snapshots, so delete them
once the upgrade is verified:

```sh
aws ec2 describe-snapshots --profile workloads-readonly --owner-ids self \
  --filters '[{"Name":"description","Values":["foundry pre-upgrade"]}]' \
  --query 'Snapshots[].[SnapshotId,VolumeId,StartTime,State]' --output table
aws ec2 delete-snapshot --profile workloads-admin --snapshot-id <snapshot id>
```

## Restore from a snapshot

Restore by copying files back from a volume made from a snapshot. That volume
has the same ext4 label and UUID as the live data volume, so mount it only by
its own device link, read-only. `/foundry` is mounted by volume ID, so the copy
can't take its place.

1. Pick a completed snapshot of the data volume:

   ```sh
   aws ec2 describe-snapshots --profile workloads-readonly --owner-ids self \
     --filters Name=volume-id,Values=vol-01ef214b56c69e2ad Name=status,Values=completed \
     --query 'sort_by(Snapshots,&StartTime)[].[SnapshotId,StartTime,Description]' \
     --output table
   ```

2. Create a volume from it in the host's Availability Zone, and attach it:

   ```sh
   aws ec2 create-volume --profile workloads-admin --snapshot-id <snapshot id> \
     --availability-zone us-west-2a --volume-type gp3 \
     --tag-specifications 'ResourceType=volume,Tags=[{Key=project,Value=foundry},{Key=purpose,Value=restore}]' \
     --query VolumeId --output text
   aws ec2 wait volume-available --profile workloads-admin --volume-ids <volume id>
   aws ec2 attach-volume --profile workloads-admin --volume-id <volume id> \
     --instance-id i-05dfb9abbc1caac32 --device /dev/sdg
   ```

3. On the host, mount it read-only by its device link. The link ends in the
   volume ID without the dash. `noload` skips the journal replay, which would
   otherwise write to the volume.

   ```sh
   RESTORE_DEV=/dev/disk/by-id/nvme-Amazon_Elastic_Block_Store_<volume id without the dash>
   lsblk -o NAME,SIZE,FSTYPE,LABEL,SERIAL "$RESTORE_DEV"
   sudo mkdir -p /mnt/restore
   sudo mount -o ro,noload "$RESTORE_DEV" /mnt/restore
   sudo ls /mnt/restore/foundrydata/Data/worlds /mnt/restore/foundrydata/Config
   ```

4. Copy back what you need while Foundry is stopped. For one world, move the
   current copy out of `Data/worlds` first, so Foundry doesn't see two:

   ```sh
   sudo systemctl stop foundry.service
   sudo mv /foundry/foundrydata/Data/worlds/<world> /foundry/<world>.before-restore
   sudo cp -a /mnt/restore/foundrydata/Data/worlds/<world> /foundry/foundrydata/Data/worlds/
   sudo systemctl start foundry.service
   ```

   Check the world as GM, then delete `/foundry/<world>.before-restore`.

5. Clean up. Unmount, detach and delete the restore volume:

   ```sh
   sudo umount /mnt/restore
   aws ec2 detach-volume --profile workloads-admin --volume-id <volume id>
   aws ec2 wait volume-available --profile workloads-admin --volume-ids <volume id>
   aws ec2 delete-volume --profile workloads-admin --volume-id <volume id>
   ```

   The restore volume carries `project=foundry` (aws-design G10), the tag the
   DLM policy selects volumes by. If the volume existed during a policy run
   (daily from 10:00 UTC, weekly from Sunday 11:00 UTC, each starting within
   an hour), DLM took snapshots of it. DLM stops managing them once the volume
   is deleted, so delete them by hand; with the volume gone, no new ones can
   appear. This filter matches only snapshots of the restore volume, never the
   data volume snapshot you restored from:

   ```sh
   aws ec2 describe-snapshots --profile workloads-readonly --owner-ids self \
     --filters Name=volume-id,Values=<volume id> \
     --query 'Snapshots[].[SnapshotId,StartTime,State]' --output table
   aws ec2 delete-snapshot --profile workloads-admin --snapshot-id <snapshot id>
   ```

Replacing the whole data volume is an OpenTofu change, because the volume has
`prevent_destroy`; plan it separately.

## License after a rebuild

`Config/license.json` holds the license key and a signature bound to the
machine. After a move or a rebuild the key stays, but the signature no longer
matches, so Foundry shows its license page until the license is signed again:

1. Open `https://foundry.craigdevjohnson.com:30000/license`.
2. Accept the end-user license agreement. Foundry signs the key in
   `license.json` for the new host.

If `license.json` is lost, the key is also in workloads Parameter Store as
`/foundry/license-key` (SecureString). Craig reads it in the console and
enters it on `/license`. It never goes in Git, chat or logs.

## Move record

Foundry moved from the hand-built host in the management account to this host
in refactor Phases 7-9 ([aws-setup #30](https://github.com/CraigDevJohnson/aws-setup/issues/30);
its plan, `docs/refactor-plan.md`, is in that repo's history).

- **2026-09-29, cutover.** `/foundry` and `/etc/letsencrypt` were copied from
  the old host through the temporary `foundry-migration-793680745829` bucket,
  because snapshots under the default `aws/ebs` key can't be shared across
  accounts. At cutover `foundrydata/Data` held 35,756 files. The world folders
  from the "Before Migration v14" snapshot were restored under
  `foundrydata/Backups/pre-v14` (decision D21). Craig accepted the license
  agreement on `/license`, which re-signed the license for the new host.
- **2026-09-29 11:12 UTC, DNS switch.** The Cloudflare `foundry` record changed
  from a CNAME to the old host's EC2 name (Elastic IP 52.42.5.45) to the
  DNS-only A record for `52.27.248.145`. `certbot renew --dry-run` then
  succeeded on the new host.
- **2026-09-29 20:54 UTC, old host stopped.** `i-01b0381fb22db508f` in
  management (180294223248).
- **2026-09-29, Phase 9.** A restore test from a post-migration snapshot of the
  data volume passed. The world folders and user files of the old "pre-character
  updates" snapshot were copied to `foundrydata/Backups/pre-character-updates`.
  The old host, its hand-built resources and all 8 management snapshots were
  deleted, and foundry #18 removed the interim OpenTofu resources
  (`mgmt_interim.tf`) and the transfer bucket (`migration.tf`)
  ([aws-setup #30](https://github.com/CraigDevJohnson/aws-setup/issues/30)).

New host at the move: instance `i-05dfb9abbc1caac32` (t4g.small, us-west-2a),
Elastic IP `52.27.248.145` (`eipalloc-03508fd8e37c5f068`), data volume
`vol-01ef214b56c69e2ad`, root volume `vol-0956156f289881032`, security group
`sg-0aa47e89292e89781`, DLM policy `policy-0fb1290b3a1a8478c`.

## The Lobby's controller

The Lobby's Foundry Tool reads the host's state from the controller,
`foundry-dev-controller` and `foundry-prod-controller`, one per site
environment (`infra/controller.tf`; deploying it is in
[infra](../infra/README.md#the-controller)): EC2's state, the SSM
agent's connectivity, whether `/join` answers and whether a stop is under way.
A Member with the site's `foundry` or `foundry-admin` grant can also start and stop the host
through it ([ADR 0006](adr/0006-the-lobby-starts-and-stops-the-host.md),
[status API](status-api.md)), though no Lobby page offers that yet: `/foundry`
shows only the status. A start is an EC2 start. A stop, once the Member
confirms nobody is playing, sends the `foundry-stop` document: the same
shutdown as `host:stop` (above) and, only if that succeeds, a power-off a
minute later, which EC2 turns into a stop because the host's
instance-initiated shutdown behavior is `stop`. If the shutdown fails, the
host stays on, as with `host:stop`.

A failed, timed-out or cancelled `foundry-stop` emails through the workloads
`alerts` topic (the `foundry-stop-failed` EventBridge rule). Foundry may then be
down while the host runs. Read the command's output, then stop it with
`task host:stop` or the Host control workflow, or start Foundry again. The
alert names the command (`command-id`), and the host's `foundry:stop-command`
tag holds the latest one. Reading command output uses `workloads-admin`, so an
agent asks Craig first:

```sh
aws ec2 describe-instances --instance-ids i-05dfb9abbc1caac32 \
  --query 'Reservations[0].Instances[0].Tags[?starts_with(Key, `foundry:stop`)]' \
  --output table --profile workloads-admin
aws ssm get-command-invocation --command-id <id> --instance-id i-05dfb9abbc1caac32 --profile workloads-admin
```

Each start and stop is a `start` or `stop` line in the function's log, with
who asked: the Member the site named, or the caller's own role session.

The `foundry-<env>-controller-failing` alarm emails through the workloads
`alerts` topic when the function crashed, timed out, or could not start, for
example with an invalid `FOUNDRY_*` setting. The page then can't show the
status. The function's lines are in `/aws/lambda/foundry-<env>-controller`
(kept 30 days). Reading them uses `workloads-admin`, so an agent asks Craig
first:

```sh
aws logs tail /aws/lambda/foundry-prod-controller --profile workloads-admin --since 1h
```

A rebuild changes the instance ID the functions watch. The rebuild's Deploy
plan updates them, so apply it in full.
