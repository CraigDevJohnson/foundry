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
read or nothing. The digest covers the commit the plan was made from, so an
apply runs only the code Craig planned, including what a plan doesn't show,
such as a provisioner's commands. It also covers each changed resource's
actions, the object it acts on, its planned values, any import or move into
it and each changed output, and leaves out
what only describes the present, so starting or stopping Foundry between the
two runs doesn't change it unless the plan changes the host. A host rebuild
(`-replace=aws_instance.host`) is a Deploy option the digest covers too. An
apply that would stop the host runs only while EC2 reports it stopped, so
Foundry's own clean shutdown (Host control's stop) has run first; the stop
OpenTofu makes doesn't run it. That covers replacing or removing the host,
detaching its data volume, and changing a setting the AWS provider changes
only on a stopped instance, such as its type.

The repository is public, so its run logs are too. Only the resource
headers, the progress lines without their `[id=...]` suffix and the totals
reach the log and the summary; attribute values stay in a file on the runner.
To read the values, Craig runs the same plan from the same commit on his
computer, where `scripts/deploy.sh` prints the whole plan.

## The deploy role

The job assumes `foundry-deployer` through GitHub OIDC. Its trust takes only
the token of a job in this repository's `deploy` Environment, with the exact
`aud` and the immutable `sub`
(`repo:CraigDevJohnson@42454849/foundry@1361164956:environment:deploy`).
What it may do is set by rules that hold whatever a commit to `main` says:

- **State.** It reads and writes only the `infra` root's state key and lock
  file, and only reads the bucket's settings. Because it can write that
  state, every `init` of the root, in the workflow or on a computer, uses
  `-lockfile=readonly`: a plain `init` would install any provider the state
  names, and a plan with an admin sign-in would then run it.
- **IAM.** It never makes, deletes or re-trusts a role. IAM has no
  condition key on a trust policy, so a role that could make one could leave
  behind a role an outside account assumes after the run has ended. The
  controllers' roles and `foundry-host-control-ci` are made by
  `infra/bootstrap`, with their trust policies, by Craig. The deploy role
  only sets those three roles' inline policies, and only while they carry
  the `foundry-workload-boundary` permissions boundary, which it can never
  remove. The boundary allows only what those roles need on hosts tagged
  `project=foundry`, Foundry's own documents and log streams, and never IAM,
  a parameter or a key. It never assumes a role.
- **The host's role.** It may pass `foundry-host` to EC2, so it can rebuild
  the host, but never change the role: it reads Foundry's SecureStrings,
  which the boundary doesn't allow.
- **The snapshot policy.** It only reads the snapshot policy and never
  passes its role, `foundry-dlm`. A lifecycle policy snapshots every volume
  its target tags match and can share the snapshots with other accounts,
  and Data Lifecycle Manager has no condition key that limits either, so a
  role that could change Foundry's policy could copy any volume in the
  account out of it.
- **What the host can reach.** Because it rebuilds the host, sets the host's
  user data and writes the `foundry-stop` document the host runs, a run can
  have the host do anything the host's role allows, including reading
  Foundry's SecureStrings. Code in a run could also leave that reach behind
  after the run: a second instance with the host's role, or a change to the
  host's user data, which the plan ignores. No later plan would show either.
  The role's own rules don't stop that; what does is that only a run Craig
  approves, of a plan he read from a commit on `main`, holds the role.
- **EC2.** Only the calls OpenTofu makes for the host, its data volume,
  Elastic IP and security group, in us-west-2, recorded from the provider's
  own requests, reads included. It reads an instance's user data, which can
  hold another workload's secrets, only from Foundry's instances. Each instance, volume, security group, rule or Elastic IP it
  creates must carry `project=foundry` in the request, a new host or rule may
  use only Foundry's security group, and it changes, tags or deletes only a
  resource already tagged `project=foundry`. It sets that tag only on a
  resource it is creating. It can make no other kind of EC2 resource, and
  never changes the default VPC's network, a network interface's security
  groups, a snapshot, an image or an account-wide EC2 setting. EC2 checks no
  tag on a network interface in the two calls that can name an existing
  one. So associating an Elastic IP could move Foundry's address onto
  another host's interface, though never another address, and launching a
  host could use another workload's unattached interface, taking its
  address and security groups.
- **The rest.** Only `foundry-*` functions, log groups, alarms and documents
  and the `foundry-stop-failed` rule. A Function URL may only be `AWS_IAM`,
  an invoke grant may name only The Lobby's `site-dev-server` and
  `site-prod-server`, and alarms and the rule may notify only the `alerts`
  topic.
- It never reads a parameter or secret, sends a command or opens a session.

The role and the boundary live in their own root, `infra/bootstrap`, which
Craig applies with `workloads-admin`, once and for each later change to it.
So do changes to `foundry-host`, `foundry-dlm`, the snapshot policy and the
state bucket's settings, the few things the deploy role can't change. The
offline tests in `infra/bootstrap/tests` check the rules above against the
plan.

## Considered options

- Apply on every merge to `main`, as The Lobby's dev environment does.
  Rejected: Craig chose to approve each run, and every run here changes the
  one host the players use.
- Keep the repository private and gate the workflow by hand. Rejected by
  Craig's choice: without a required reviewer, nothing in GitHub holds a run
  for his approval.
- The deploy role without a boundary on the roles whose policies it sets.
  Rejected: it could give a role any permission and then act through it.
- The deploy role making the controllers' roles inside the boundary.
  Rejected: IAM can't limit the trust policy of a role it makes, so a run
  could make one an outside account assumes, with the boundary's reach to
  the host, and nothing in the infra root's state would show it.
- The deploy role as JSON policy files created by hand, as minecraft's
  operator is. Rejected: a root with tests keeps the rules checked on every
  pull request, and the role's limits next to the code they limit.
