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
checks that no `/foundry/foundryvtt/main.js` process is left. If the script is
missing, the service stop or `sync` fails, or Foundry is still running, it leaves the host
running and exits non-zero. A failing script alone doesn't stop it, since the
service stop and the process check decide. Stopping disconnects everyone, so
check that nobody is playing first. `host:stop` does nothing if the host is
already stopped; `host:start` on a running host only waits for Foundry. `foundry.service` is enabled, so Foundry starts
with the host.

The **Host control** workflow in GitHub Actions runs the same tasks. Start it
from the Actions tab, pick `status`, `start` or `stop`, and for `stop` tick
**nobody playing**. It runs in the `foundry-host` GitHub Environment and
assumes `foundry-host-control-ci` (`infra/ci.tf`) through the workloads GitHub
OIDC provider. The role can only describe instances, start and stop this
instance, and run the `foundry-shutdown` document on it, never arbitrary
commands. Setup, once the role is applied:

1. Create the `foundry-host` environment under the repository's
   Settings > Environments. Set **Deployment branches** to `main` only: the
   role's trust policy pins the environment, not the branch, so this rule is
   what keeps a workflow from another branch from getting the role. No
   required reviewers: runs start without an approval step (Craig's call,
   2026-10-09).
2. Add the variable `AWS_HOST_CONTROL_ROLE_ARN` with the value of
   `tofu -chdir=infra output -raw host_control_role_arn`.

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
itself on port 80. The current certificate expires on 2026-11-17.

- `certbot-renew.timer` must be enabled. On the old host it was found
  disabled.
- The timer runs daily at 10:00 UTC with up to 15 minutes of random delay, set
  in `/etc/systemd/system/certbot-renew.timer.d/override.conf`. Certbot renews
  only when the certificate is close to expiry, by default about 30 days
  before.
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
systemctl list-timers certbot-renew.timer --no-pager    # shows the next run
journalctl -u certbot-renew.service -n 20 --no-pager    # recent runs
```

```sh
aws ssm send-command --profile workloads-admin \
  --instance-ids i-05dfb9abbc1caac32 --document-name AWS-RunShellScript \
  --parameters '{"commands":["systemctl is-enabled certbot-renew.timer","systemctl list-timers certbot-renew.timer --no-pager","journalctl -u certbot-renew.service -n 20 --no-pager"]}' \
  --query Command.CommandId --output text
```

From anywhere, this prints the served certificate's expiry. If it still shows
2026-11-17 after about 2026-10-25, the renewal failed: read the journal.

```sh
openssl s_client -connect foundry.craigdevjohnson.com:30000 \
  -servername foundry.craigdevjohnson.com </dev/null 2>/dev/null |
  openssl x509 -noout -enddate
```

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
in refactor Phases 7-9 (aws-setup `docs/refactor-plan.md`).

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
  management (180294223248). Refactor Phase 9 deletes it and its hand-built
  resources, and removes the interim OpenTofu resources (`mgmt_interim.tf`)
  and the transfer bucket (`migration.tf`).

New host at the move: instance `i-05dfb9abbc1caac32` (t4g.small, us-west-2a),
Elastic IP `52.27.248.145` (`eipalloc-03508fd8e37c5f068`), data volume
`vol-01ef214b56c69e2ad`, root volume `vol-0956156f289881032`, security group
`sg-0aa47e89292e89781`, DLM policy `policy-0fb1290b3a1a8478c`.
