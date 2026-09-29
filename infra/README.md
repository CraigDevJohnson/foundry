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
| `variables.tf` | `keep_cloudwatch_agent` (refactor decision D7, default `false`). Change it only by committing a new default, never with `-var`, so every plan sees the same value |

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
