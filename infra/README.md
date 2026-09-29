# Foundry infrastructure

The OpenTofu root for Foundry's AWS resources, as set out in aws-setup's
[aws-design §6.2](https://github.com/CraigDevJohnson/aws-setup/blob/main/aws-design.md#62-foundry-game-server-repo-foundry)
and §10. OpenTofu 1.12.x, AWS provider 6.x.

| File | Holds |
|---|---|
| `backend.tf` | State in `foundry-tofu-state-793680745829` (workloads, us-west-2), key `foundry/terraform.tfstate`, native S3 lockfile, profile `workloads-admin` |
| `providers.tf` | Default provider: workloads (`workloads-admin`, 793680745829). Alias `aws.mgmt`: management (`mgmt-admin`, 180294223248), for the interim resources only. Both tag `project=foundry` |
| `state.tf` | The state bucket, imported after it is created by CLI, with `prevent_destroy` and a TLS-only bucket policy |
| `mgmt_interim.tf` | Interim protection for the current host in management: the `foundry-dlm` role, the snapshot policy on both volumes (the plan fails if either volume lacks `project=foundry`), and the least-privilege `foundry` host role and instance profile |
| `network.tf` | Default VPC, its default subnet in AZ ID `usw2-az2` (us-west-2a in workloads), and the `foundry` security group: 80/tcp and 30000/tcp from `0.0.0.0/0`, all IPv4 egress, no SSH |
| `host.tf` | The workloads host: `foundry-host` role (SSM default instance policy, `ssm:GetParameter*` on `parameter/foundry` and `parameter/foundry/*` only, no KMS) and instance profile, the t4g.small instance (IMDSv2, no key pair, encrypted 10 GiB gp3 root), the 20 GiB `foundry-data` volume with `prevent_destroy`, the Elastic IP, and the `StatusCheckFailed` alarm to the workloads `alerts` topic (D18) |
| `backups.tf` | The workloads `foundry-dlm` role and snapshot policy: both volumes tagged `project=foundry`, daily at 10:00 UTC keeping 7, weekly on Sunday at 11:00 UTC keeping 4 |
| `migration.tf` | **Temporary** (Phases 7-9): the `foundry-migration-793680745829` transfer bucket, the host's `foundry-migration-get` read grant, and the old host's `foundry-migration-put` upload grant |
| `templates/` | First-boot user data (no secrets, never formats a disk) and the certbot deploy hook it installs |
| `outputs.tf` | Instance, Elastic IP, data volume, Availability Zone and security group IDs for the runbook |
| `variables.tf` | `keep_cloudwatch_agent` (refactor decision D7, default `false`), and `ami_id` (AL2023 arm64; the instance ignores later changes). Change them only by committing a new default, never with `-var`, so every plan sees the same value |

## Commands

Offline checks, also run by CI, need no AWS credentials:

```sh
tofu fmt -check -recursive infra
tofu -chdir=infra init -backend=false
tofu -chdir=infra validate
```

A plan needs an Identity Center session (`aws sso login --sso-session identity-center`)
because the backend and providers use the admin profiles pinned above:

```sh
tofu -chdir=infra init
tofu -chdir=infra plan -out=f1.plan
```

Apply only a saved plan that Craig has approved: `tofu -chdir=infra apply f1.plan`.
The committed `.claude/settings.json` asks before any apply, import, state,
destroy or force-unlock command, and before any command that names an admin
profile.

## Interim resources and the move

Foundry still runs on a hand-built host in management, so `mgmt_interim.tf`
protects it there while state already lives in workloads. When Foundry moves,
the workloads host and its own snapshot policy are added on the default
provider and verified; then `mgmt_interim.tf` is removed and applied, which
deletes the interim policy, `foundry-dlm` and the old `foundry` role. Never
switch the `aws.mgmt` provider to workloads in place: that would orphan the
management copies.

The first plan shows the two `*_exclusive` resources as creates, but applying
them also removes what they don't list: it detaches
`AmazonSSMManagedInstanceCore` (and `CloudWatchAgentServerPolicy` unless D7
keeps it) and deletes the inline `foundry-server-role`. Those removals don't
appear as destroys in the plan.

The resources were imported on the first apply (refactor Phase 3, 2026-09-29), and the
`import` blocks have since been removed. `infra/tests/foundry.tftest.hcl` checks the plan
offline with mocked providers: `tofu -chdir=infra test` after `init -backend=false`.

## Manual snapshot before upgrades

The daily and weekly snapshots cover both volumes. Before a Foundry or host
upgrade, also take a manual snapshot of both volumes and keep it until the
upgrade is verified:

```sh
aws ec2 create-snapshots --profile mgmt-admin \
  --instance-specification InstanceId=i-01b0381fb22db508f,ExcludeBootVolume=false \
  --copy-tags-from-source volume --description "foundry pre-upgrade"
```

This changes AWS, so an agent runs it only as an approved change.

After the move, the same command for the workloads host is:

```sh
aws ec2 create-snapshots --profile workloads-admin \
  --instance-specification InstanceId=$(tofu -chdir=infra output -raw host_instance_id),ExcludeBootVolume=false \
  --copy-tags-from-source volume --description "foundry pre-upgrade"
```

## Moving to the workloads host (refactor Phases 7-9)

The full steps and approvals are in aws-setup's `docs/refactor-plan.md`. This
is the repo-side summary. Every step that changes AWS or the host runs only
as an approved change.

**Phase 7: build.** Apply the approved plan: creates only, plus one in-place
change to the old `foundry` role's inline list (`foundry-migration-put`). Then
format the new data volume **once**, through Run Command with `workloads-admin`:

1. `lsblk -o NAME,SIZE,FSTYPE,LABEL,SERIAL` must show the 20 GiB device whose
   serial is `tofu output data_volume_id` without the dash, with no filesystem.
   If it has one, stop: never format a volume that holds data.
2. `mkfs.ext4 -L foundry-data /dev/<dev>`, then `mount -a` and `findmnt /foundry`.

User data has already installed Node.js 24 and certbot, the `foundry` user
(UID/GID 992 as on the old host, when free), the fstab entry, `foundry.service` (not
enabled), the renewal timer (enabled, daily 10:00 UTC with 15 minutes of
jitter) and the deploy hook. `cloud-init status` must report `done`.

**Phase 8: restore and DNS.** Craig copies `/foundry` and `/etc/letsencrypt`
from the old host through the transfer bucket and restores them on the new
host (the plan has the commands; add `--region us-west-2` if the CLI has no
Region). The copies expire from the bucket after 7 days. After the restore,
in a `workloads-admin` session:

```sh
sudo chown -R foundry:foundry /foundry
sudo rm -f /etc/letsencrypt/renewal-hooks/deploy/foundry-copy.sh
# The restored /etc/letsencrypt can replace the hook link; put it back.
sudo ln -sfn /usr/local/sbin/foundry-cert-deploy /etc/letsencrypt/renewal-hooks/deploy/foundry.sh
ls /etc/letsencrypt/renewal-hooks/deploy/        # only foundry.sh
sudo /usr/local/sbin/foundry-cert-deploy         # installs Config/foundry.pem and foundry.key
sudo systemctl enable --now foundry.service
```

Before DNS changes, `curl -sS -o /dev/null -w '%{http_code}' --resolve
foundry.craigdevjohnson.com:30000:<EIP> https://foundry.craigdevjohnson.com:30000/join`
must return 200. Then set the Cloudflare `foundry` record to a DNS-only A
record for `tofu output host_public_ip` (D19), and run
`sudo certbot renew --dry-run` once DNS points at the new address.

**Phase 9: remove the management copies.** After the restore test, delete
`migration.tf` and `mgmt_interim.tf` together (the interim role's inline list
refers to `foundry-migration-put`), drop their test runs and the `aws.mgmt`
mock from `tests/foundry.tftest.hcl`, and apply a plan that shows only those
destroys. Keep the `aws.mgmt` provider for that apply and remove it in a
follow-up commit.

**Rebuilding the host later.** `tofu plan -replace=aws_instance.host` keeps
the data volume and the Elastic IP. The new root volume has no certificate,
so after user data finishes, issue one (port 80 is open; the hook directory
runs only on renewals, so pass the hook) and start Foundry:
`sudo certbot certonly --standalone -d foundry.craigdevjohnson.com --deploy-hook /usr/local/sbin/foundry-cert-deploy`,
then `sudo systemctl enable --now foundry.service`.
