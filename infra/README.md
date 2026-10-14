# Foundry infrastructure

The OpenTofu root for Foundry's AWS resources, as set out in aws-setup's
[aws-design §6.2](https://github.com/CraigDevJohnson/aws-setup/blob/main/aws-design.md#62-foundry-game-server-repo-foundry)
and §10. OpenTofu 1.12.x, AWS provider 6.x. The Deploy workflow applies it
after Craig approves the run ([Deploying](#deploying), [ADR 0007](../docs/adr/0007-deploy-from-github-actions.md)).
Day-to-day operation of the host (DNS, certificate renewal, snapshots,
restores and the license) is in [docs/operations.md](../docs/operations.md).

| File | Holds |
|---|---|
| `backend.tf` | State in `foundry-tofu-state-793680745829` (workloads, us-west-2), key `foundry/terraform.tfstate`, native S3 lockfile, profile `var.aws_profile` |
| `providers.tf` | The workloads provider (`var.aws_profile`, 793680745829), tagging `project=foundry` and ignoring the controller's stop tags (`foundry:stop-command`, `foundry:stop-requested`) |
| `state.tf` | The state bucket, imported after it is created by CLI, with `prevent_destroy` and a TLS-only bucket policy |
| `network.tf` | Default VPC, its default subnet in AZ ID `usw2-az2` (us-west-2a in workloads), and the `foundry` security group: 80/tcp and 30000/tcp from `0.0.0.0/0`, all IPv4 egress, no SSH |
| `host.tf` | The workloads host: `foundry-host` role (SSM default instance policy, `ssm:GetParameter*` on `parameter/foundry` and `parameter/foundry/*` only, no KMS; exclusive lists for both managed and inline policies) and instance profile, the t4g.small instance (IMDSv2, no key pair, encrypted 10 GiB gp3 root), the 20 GiB `foundry-data` volume with `prevent_destroy`, the Elastic IP with `prevent_destroy`, and the `StatusCheckFailed` alarm to the workloads `alerts` topic (D18) |
| `backups.tf` | The workloads `foundry-dlm` role and snapshot policy: both volumes tagged `project=foundry`, daily at 10:00 UTC keeping 7, weekly on Sunday at 11:00 UTC keeping 4 |
| `ci.tf` | The `foundry-host-control-ci` role for the Host control workflow, inside `foundry-workload-boundary`: trusts the workloads GitHub OIDC provider (owned by aws-setup) only for this repository's `foundry-host` environment, and may only describe instances, start and stop the host, and run the parameterless `foundry-shutdown` SSM document (`templates/foundry-shutdown.sh`) on it |
| `controller.tf` | The controller The Lobby's Foundry Tool calls ([ADR 0005](../docs/adr/0005-the-lobby-calls-the-controller-through-a-function-url.md), [ADR 0006](../docs/adr/0006-the-lobby-starts-and-stops-the-host.md)), once per site environment: the `foundry-<env>-controller` Lambda (Go, provided.al2023, arm64) with its log group (30 days), its role inside `foundry-workload-boundary` (own log streams; `ec2:DescribeInstances`, `ssm:DescribeInstanceInformation` and `ssm:GetCommandInvocation`; `ec2:StartInstances` on the host; `ssm:SendCommand` of `foundry-stop` to the host; `ec2:CreateTags` on the host for the two stop tags only; exclusive lists for both managed and inline policies), an `AWS_IAM` Function URL granted only to `site-<env>-server`, and the `foundry-<env>-controller-failing` alarm on Lambda errors to the workloads `alerts` topic. Shared by both: the parameterless `foundry-stop` document (`templates/foundry-stop.sh.tftpl`: it runs `templates/foundry-shutdown.sh` from a file of its own, then `templates/foundry-power-off.sh` only if the shutdown exits 0) and the `foundry-stop-failed` EventBridge rule, which sends a failed, timed-out or cancelled stop to the same topic |
| `templates/` | The `foundry-shutdown` script, first-boot user data (no secrets, never formats a disk) and the certbot deploy hook it installs |
| `outputs.tf` | Instance, Elastic IP, root volume, data volume, Availability Zone and security group IDs for the runbook, the Host control role ARN, and the controller's URLs and function ARNs per site environment for the-lobby |
| `variables.tf` | `ami_id` (AL2023 arm64; the instance ignores later changes). Change it only by committing a new default, never with `-var`, so every plan sees the same value. `controller_package_path`, the controller's zip, relative to `infra/` (default `../dist/bootstrap.zip`, which `task build` writes). `aws_profile`, the named profile for the backend and provider (default `workloads-admin`) |
| `deploy.tfvars` | The Deploy workflow's inputs: `aws_profile = null`, so OpenTofu uses the deploy role's credentials from the environment |
| `bootstrap/` | Its own root, applied by Craig: the `foundry-deployer` role the Deploy workflow runs as and the `foundry-workload-boundary` permissions boundary ([The deploy role](#the-deploy-role)). State key `foundry/bootstrap/terraform.tfstate` in the same bucket |

## Commands

Offline checks, also run by CI, need no AWS credentials. The tests in
`tests/foundry.tftest.hcl` check the plan with a mocked provider, and
`tests/fixtures/bootstrap.zip` stands in for the controller's zip. The tests
in `bootstrap/tests` check the deploy role's limits the same way:

```sh
tofu fmt -check -recursive infra
tofu -chdir=infra init -backend=false
tofu -chdir=infra validate
tofu -chdir=infra test
tofu -chdir=infra/bootstrap init -backend=false
tofu -chdir=infra/bootstrap validate
tofu -chdir=infra/bootstrap test
```

A plan from a computer, to look before a Deploy run, needs an Identity Center
session (`aws sso login --sso-session identity-center`) because the backend
and providers default to the admin profile. It also needs the controller's
zip, so run `task build` first:

```sh
tofu -chdir=infra init
tofu -chdir=infra plan
```

Changes to `infra` are applied by the Deploy workflow, not from a computer.
The committed `.claude/settings.json` asks before any apply, import, state,
destroy or force-unlock command, and before any command that names an admin
profile.

## Deploying

Run **Actions › Deploy › Run workflow** on `main`:

1. Choose mode `plan` and run it. Approve the run when GitHub asks (the
   `deploy` environment's review). Read the plan in the run's summary: the
   resources it changes and the totals, then its digest.
2. Run Deploy again with mode `apply` and that digest as `plan`, and approve
   it. It plans again and applies only if the plan is the one you read; if
   `main` or the state changed since, it applies nothing and posts the new
   plan and digest.

**Rebuild host** replaces the host's instance (see [Rebuilding the host](#rebuilding-the-host)).
Check it on both runs: the digest covers it.

## The deploy role

`bootstrap/` holds the `foundry-deployer` role and the
`foundry-workload-boundary` boundary on the roles it makes. It is applied by
Craig with `workloads-admin`, once before the first Deploy run and again for
each change to it. So are changes to the host's role (`foundry-host`), the
snapshot role (`foundry-dlm`) and the state bucket's settings, which the
deploy role can't make: a Deploy apply that changes one stops with
`AccessDenied` at that resource.

One time, in GitHub, at Settings › Environments, after the repository is
public:

1. **New environment** named `deploy`.
2. Under **Deployment protection rules**, tick **Required reviewers**, add
   yourself and click **Save protection rules**. Leave **Prevent
   self-review** unticked: you start every run yourself.
3. Under **Deployment branches and tags**, choose **Selected branches and
   tags** and add the rule `main`.

Then, on a computer with an Identity Center session, from the repository root:

```sh
tofu -chdir=infra/bootstrap init
tofu -chdir=infra/bootstrap plan -out=bootstrap.tfplan
```

The first plan adds 5: the boundary, the role, its inline policy and its
two exclusive lists. After Craig approves it,
`tofu -chdir=infra/bootstrap apply bootstrap.tfplan` prints the role's ARN,
`arn:aws:iam::793680745829:role/foundry-deployer`, which the workflow names.

The first Deploy plan after that also changes `foundry-host-control-ci` in
place, to put it inside the boundary; the controllers are created inside it.

## The controller

`controller.tf` adds `foundry-dev-controller` and `foundry-prod-controller`,
which answer The Lobby's Foundry Tool (the HTTP contract is in
[status-api.md](../docs/status-api.md)). Each is granted only to its site's
role, `site-dev-server` or `site-prod-server`. Both roles must already exist:
Lambda refuses a grant to a role it can't find.

1. The Deploy workflow builds the zip with `task build`, which writes
   `dist/bootstrap.zip`. The same source gives the same zip, so a later plan
   after an unchanged build shows no change to the functions.
2. Run a Deploy plan. The first plan adds 23 resources: 10 per environment (the
   log group, the role, its inline policy, the two exclusive lists, the
   function, its Function URL, the two grants to the site role and the alarm),
   plus the `foundry-stop` document and the `foundry-stop-failed` rule and its
   target. Besides the boundary on `foundry-host-control-ci`, it should change
   nothing else. `aws_instance.host` now states
   `instance_initiated_shutdown_behavior = "stop"`, EC2's default; if the plan
   shows any change to the host, stop and look before applying.
3. Run a Deploy apply with the plan's digest.
4. Check each environment with signed requests, from a computer. AWS lets the
   admin profile call the URL on its own permissions, so it can sign them.
   This uses `workloads-admin`, so an agent runs it only as part of the
   approved change.
   The signed calls run in a subshell: exported keys take precedence over
   `AWS_PROFILE`, so left in the shell they would turn later
   `AWS_PROFILE=workloads-readonly` commands into admin calls.

   ```sh
   url=$(tofu -chdir=infra output -json controller_urls | jq -r .dev); echo "$url"
   curl -sS -o /dev/null -w '%{http_code}\n' "${url}server"
   (
     eval "$(aws configure export-credentials --profile workloads-admin --format env)"
     curl -sS -o /dev/null -w '%{http_code}\n' --aws-sigv4 "aws:amz:us-west-2:lambda" \
       --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" -H "x-amz-security-token: $AWS_SESSION_TOKEN" \
       "${url}healthz"
     curl -sS --aws-sigv4 "aws:amz:us-west-2:lambda" \
       --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" -H "x-amz-security-token: $AWS_SESSION_TOKEN" \
       "${url}server"; echo
   )
   ```

   This prints the address, `https://<id>.lambda-url.us-west-2.on.aws/`, then
   `403` (an unsigned call is refused), then `204`, then the status JSON, such
   as `{"state":"ready","join_url":"https://foundry.craigdevjohnson.com:30000/join",...}`.
   Repeat with `.prod`. The check never starts or stops the host: those are
   tried through The Lobby, as their own change.
5. Connect the site. In the-lobby, set `foundry_backend_url` and
   `foundry_backend_function_arn` in `infra/environments/dev/dev.tfvars` to the
   `dev` values of `tofu -chdir=infra output controller_urls` and
   `controller_function_arns`, and the `prod` values in
   `infra/environments/prod/prod.tfvars`, then deploy the site as that
   repository describes.

The functions take the instance ID from `aws_instance.host`, so a rebuild plan
also updates both. While that apply runs, the page reports no server.

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

A Deploy plan with **Rebuild host** checked (`-replace=aws_instance.host`)
keeps the data volume and the Elastic IP (both have `prevent_destroy`), and
its apply needs the box checked again. The volume attaches only after the new
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
