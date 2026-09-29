# Foundry infrastructure

The OpenTofu root for Foundry's AWS resources, as set out in aws-setup's
[aws-design §6.2](https://github.com/CraigDevJohnson/aws-setup/blob/main/aws-design.md#62-foundry-game-server-repo-foundry)
and §10. OpenTofu 1.12.x, AWS provider 6.x. Day-to-day operation of the host
(DNS, certificate renewal, snapshots, restores and the license) is in
[docs/operations.md](../docs/operations.md).

| File | Holds |
|---|---|
| `backend.tf` | State in `foundry-tofu-state-793680745829` (workloads, us-west-2), key `foundry/terraform.tfstate`, native S3 lockfile, profile `workloads-admin` |
| `providers.tf` | Default provider: workloads (`workloads-admin`, 793680745829), tagging `project=foundry`. Alias `aws.mgmt` (`mgmt-admin`, 180294223248) is used by no resource: it stays only for the refactor Phase 9 apply that destroys the old interim management resources, and a follow-up commit removes it |
| `state.tf` | The state bucket, imported after it is created by CLI, with `prevent_destroy` and a TLS-only bucket policy |
| `network.tf` | Default VPC, its default subnet in AZ ID `usw2-az2` (us-west-2a in workloads), and the `foundry` security group: 80/tcp and 30000/tcp from `0.0.0.0/0`, all IPv4 egress, no SSH |
| `host.tf` | The workloads host: `foundry-host` role (SSM default instance policy, `ssm:GetParameter*` on `parameter/foundry` and `parameter/foundry/*` only, no KMS; exclusive lists for both managed and inline policies) and instance profile, the t4g.small instance (IMDSv2, no key pair, encrypted 10 GiB gp3 root), the 20 GiB `foundry-data` volume with `prevent_destroy`, the Elastic IP with `prevent_destroy`, and the `StatusCheckFailed` alarm to the workloads `alerts` topic (D18) |
| `backups.tf` | The workloads `foundry-dlm` role and snapshot policy: both volumes tagged `project=foundry`, daily at 10:00 UTC keeping 7, weekly on Sunday at 11:00 UTC keeping 4 |
| `templates/` | First-boot user data (no secrets, never formats a disk) and the certbot deploy hook it installs |
| `outputs.tf` | Instance, Elastic IP, root volume, data volume, Availability Zone and security group IDs for the runbook |
| `variables.tf` | `ami_id` (AL2023 arm64; the instance ignores later changes). Change it only by committing a new default, never with `-var`, so every plan sees the same value |

## Commands

Offline checks, also run by CI, need no AWS credentials. The tests in
`tests/foundry.tftest.hcl` check the plan with a mocked provider:

```sh
tofu fmt -check -recursive infra
tofu -chdir=infra init -backend=false
tofu -chdir=infra validate
tofu -chdir=infra test
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

## Manual snapshot before upgrades

The daily and weekly snapshots cover both volumes. Before a Foundry or host
upgrade, also take a manual snapshot of both volumes and keep it until the
upgrade is verified:

```sh
aws ec2 create-snapshots --profile workloads-admin \
  --instance-specification InstanceId=i-05dfb9abbc1caac32,ExcludeBootVolume=false \
  --copy-tags-from-source volume --description "foundry pre-upgrade"
```

This changes AWS, so an agent runs it only as an approved change. After a
rebuild, `tofu -chdir=infra output -raw host_instance_id` prints the new
instance ID. [Operations](../docs/operations.md#snapshot-before-an-upgrade)
covers stopping Foundry first and deleting the snapshot afterwards.

## Rebuilding the host

`tofu plan -replace=aws_instance.host` keeps the data volume and the Elastic
IP (both have `prevent_destroy`). The volume attaches only after the new
instance has booted, so `/foundry` isn't mounted yet, and the new root volume
has no certificate. After user data finishes (`cloud-init status` reports
`done`):

```sh
sudo mount -a && findmnt /foundry                 # must show the data volume
sudo certbot certonly --standalone -d foundry.craigdevjohnson.com
sudo /usr/local/sbin/foundry-cert-deploy          # installs Config/foundry.pem and foundry.key
sudo systemctl enable --now foundry.service
```

Don't pass `--deploy-hook` to `certonly`: certbot saves it in the renewal
config, so each renewal would run the hook twice, once from the config and
once from the hook directory.

Then re-sign the Foundry license on the new host
([operations](../docs/operations.md#license-after-a-rebuild)), and update the
instance and root volume IDs in `docs/operations.md` and the snapshot command
above. `tofu -chdir=infra output` prints them (`host_instance_id` and
`root_volume_id`).
