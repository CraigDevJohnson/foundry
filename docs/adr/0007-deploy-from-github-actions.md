---
status: accepted
date: 2026-10-14
---

# Deploy from GitHub Actions

Deployments run in the cloud, not from Craig's computer. Craig chose how for
this repository (2026-10-14): make it public, and add a Deploy workflow that
plans, then applies only after he approves the run, like minecraft's.

The Deploy workflow (`.github/workflows/deploy.yml`) runs the `infra` root
only when Craig starts it by hand, never on a push, because every run holds
the deploy role's write credentials. Its one job runs only on `main`, only for
a run his own account started, and in the `deploy` GitHub Environment, which
only `main` may use and which has Craig as its required reviewer. GitHub
allows a required reviewer on his plan only in a public repository, so GitHub
holds every run until he approves it and only then hands the job the role's
credentials.

A plan run posts the plan and its digest to the run summary. An apply run
takes that digest, plans again and applies only a plan with the same digest
(`scripts/deploy.sh`, `cmd/plandigest`), so it applies exactly the plan Craig
read or nothing. The digest covers each changed resource's actions, the
object it acts on, its planned values, any import or move into it and each
changed output, and leaves out
what only describes the present, so starting or stopping Foundry between the
two runs doesn't change it unless the plan changes the host. A host rebuild
(`-replace=aws_instance.host`) is a Deploy option the digest covers too.

The repository is public, so its run logs are too. Only the resource
headers, the progress lines without their `[id=...]` suffix and the totals
reach the log and the summary; attribute values stay in a file on the runner.

## The deploy role

The job assumes `foundry-deployer` through GitHub OIDC. Its trust takes only
the token of a job in this repository's `deploy` Environment, with the exact
`aud` and the immutable `sub`
(`repo:CraigDevJohnson@42454849/foundry@1361164956:environment:deploy`).
What it may do is set by rules that hold whatever a commit to `main` says:

- **State.** It reads and writes only the `infra` root's state key and lock
  file, and only reads the bucket's settings.
- **IAM.** It may create or change only the controllers' roles and
  `foundry-host-control-ci`, and only inside the `foundry-workload-boundary`
  permissions boundary, which it can never remove. The boundary allows only
  what those roles need on hosts tagged `project=foundry`, Foundry's own
  documents and log streams, and never IAM, a parameter or a key. So no role
  the deploy role makes can do more than the controllers already do.
- **The host's and snapshot roles.** It may pass `foundry-host` to EC2 and
  `foundry-dlm` to Data Lifecycle Manager, so it can rebuild the host and
  keep the snapshot policy, but never change either role: the host's role
  reads Foundry's SecureStrings, which the boundary doesn't allow.
- **EC2.** Only the calls OpenTofu makes for the host, its data volume,
  Elastic IP and security group, in us-west-2, recorded from the provider's
  own requests. Each instance, volume, security group, rule or Elastic IP it
  creates must carry `project=foundry` in the request, a new host or rule may
  use only Foundry's security group, and it changes, tags or deletes only a
  resource already tagged `project=foundry`. It sets that tag only on a
  resource it is creating. It can make no other kind of EC2 resource, and
  never changes the default VPC's network, a snapshot, an image or an
  account-wide EC2 setting.
- **The rest.** Only `foundry-*` functions, log groups, alarms and documents,
  the `foundry-stop-failed` rule and the snapshot policy tagged
  `project=foundry`. A Function URL may only be `AWS_IAM`, an invoke grant may
  name only The Lobby's `site-dev-server` and `site-prod-server`, and alarms
  and the rule may notify only the `alerts` topic.
- It never reads a parameter or secret, sends a command or opens a session.

The role and the boundary live in their own root, `infra/bootstrap`, which
Craig applies with `workloads-admin`, once and for each later change to it.
So do changes to `foundry-host`, `foundry-dlm` and the state bucket's
settings, the few things the deploy role can't change. The offline tests in
`infra/bootstrap/tests` check the rules above against the plan.

## Considered options

- Apply on every merge to `main`, as The Lobby's dev environment does.
  Rejected: Craig chose to approve each run, and every run here changes the
  one host the players use.
- Keep the repository private and gate the workflow by hand. Rejected by
  Craig's choice: without a required reviewer, nothing in GitHub holds a run
  for his approval.
- The deploy role without a boundary on the roles it makes. Rejected: it
  could make a role with any permission and then act through it.
- The deploy role as JSON policy files created by hand, as minecraft's
  operator is. Rejected: a root with tests keeps the rules checked on every
  pull request, and the role's limits next to the code they limit.
