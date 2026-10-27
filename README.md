# Foundry

The management plane for Craig's hosted Foundry Virtual Tabletop (Foundry VTT)
application and the AWS EC2 instance that runs it.

This repository owns the tools for operating that installation. The first
release supports a game with at most five concurrent users. Craig operates it,
and The Lobby's site lets any Member with its `foundry` or `foundry-admin`
grant start and stop the host too
([ADR 0006](docs/adr/0006-the-lobby-starts-and-stops-the-host.md)). The Lobby
is the interface: its `/foundry` page shows invited players the host's status,
and a later page will offer start and stop. It runs at next.craigdevjohnson.com
until it takes over craigdevjohnson.com from the portfolio. This repository
owns the Foundry backend, operations, and reproducible host and service setup.
The operating policy and backend architecture were confirmed through the design
interview on 2026-09-08; the ADRs below record where the backend changed since.

## Agreed first-release scope

- View the status of the host and Foundry VTT application.
- Provide explicit Start session and End session actions: start the host for
  play or solo GM preparation, then shut Foundry down cleanly and stop the
  host. The scheduled snapshots back up the finished session.
- Back up the installation and verify that it can be recovered.
- Define how to recreate the host and service configuration. OpenTofu builds
  the host in the workloads account ([infra](infra/README.md)); Foundry moved
  to it on 2026-09-29 instead of adopting the old host.

## Operating priorities

- Preserve existing worlds and their data.
- Keep the game available during play; continuous availability outside play is
  not required.
- Schedule disruptive maintenance outside play.
- Take a manual snapshot of both volumes before upgrades. Upgrade automation
  itself has not been selected for the first release.
- Protect completed sessions with the scheduled snapshots: a finished session
  is in the next daily snapshot, up to about 24 hours later. Loss of progress
  since the last snapshot is acceptable if storage fails; recover with the
  documented restore.
- Use manual session controls initially; automatic idle shutdown and a fixed
  start/stop schedule are outside the agreed first-release scope.
- Require confirmation that nobody will be interrupted before End session.
  Automatic player detection is not required.
- If End session cannot shut Foundry down cleanly, keep the host running and
  report the failure.
- Back up both EBS volumes with a Data Lifecycle Manager policy in us-west-2:
  daily snapshots at 10:00 UTC keeping 7, and weekly snapshots on Sundays at
  11:00 UTC keeping 4 ([infra](infra/README.md)). Keep each manual pre-upgrade
  snapshot until the upgrade is verified. Recovery is best effort, about
  24 hours; there is no recovery Region, cross-Region copy, vault lock or legal
  hold ([aws-design §6.2 and §9.3](https://github.com/CraigDevJohnson/aws-setup/blob/main/aws-design.md#62-foundry-game-server-repo-foundry)).
- Test a restore once after the snapshot policy is set up. There is no
  restore-drill calendar. That test passed on 2026-09-29, from a manual
  post-migration snapshot of the data volume
  ([aws-setup #30](https://github.com/CraigDevJohnson/aws-setup/issues/30)).
- Keep costs appropriate for a small personal game server. No fixed monthly cap
  has been chosen; proposals must explain any increase to the current operating
  cost.

The controller reports the host's status and starts and stops it
([status API](docs/status-api.md)). A stop, from The Lobby or from Host
control, shuts Foundry down cleanly before the host stops: that is End session.
It takes no backup of its own. Craig dropped the backup step on 2026-10-16
([#7](https://github.com/CraigDevJohnson/foundry/issues/7)), so the scheduled
snapshots hold a finished session. Foundry moved to a new host in the
workloads account on 2026-09-29 instead of adopting the old one
([move record](docs/operations.md#move-record)): `foundry.service` runs Foundry
and starts with the host, `certbot-renew.timer` is enabled and a
`certbot renew --dry-run` passed, and a restore from a snapshot was tested.
The first real certificate renewal is due from about 2026-10-18. What is left
to prove on the live host is in
[#25](https://github.com/CraigDevJohnson/foundry/issues/25);
`scripts/live-checks.py` runs those checks from Craig's computer
([certificate renewal](docs/operations.md#certificate-renewal)).

## Project documentation

- [Domain glossary](CONTEXT.md) defines the project's terms.
- [Operations](docs/operations.md) covers running the workloads host: start
  and stop (`task host:start`, `task host:stop` and the Host control workflow), DNS,
  certificate renewal, snapshots, restores, the license and the move record.
- [Hosting baseline](docs/hosting-baseline.md) is historical: the old
  management-account host and related management tools as found in discovery.
- [Host lifecycle contract](docs/host-contract.md) defines the pinned inputs,
  filesystem paths, fixed commands, and offline checks for issue #5. The
  workloads host follows it only for its `foundry.service` template, which
  [infra/host.tf](infra/host.tf) edits.
- [Local recovery sets](docs/local-recovery-sets.md) describes the fixture-tested capture,
  integrity verification and isolated materialization tools. They are offline
  only: Data Lifecycle Manager snapshots replaced the cloud recovery sets they
  were built for.
- [Host adoption and rollback](docs/host-adoption.md) is historical: the
  adoption procedure for the old host, replaced by the move to workloads.
- [First-release specification](https://github.com/CraigDevJohnson/foundry/issues/2)
  records the original scope, acceptance checks, and adoption prerequisites. It
  closed on 2026-10-16; [#25](https://github.com/CraigDevJohnson/foundry/issues/25)
  holds the live checks still to run.
- [Portfolio and Foundry responsibilities](docs/adr/0001-portfolio-and-foundry-responsibilities.md)
  records the management boundary. Its portfolio interface is superseded by
  [The Lobby calls the controller through a Function URL](docs/adr/0005-the-lobby-calls-the-controller-through-a-function-url.md).
- [Session-based recovery](docs/adr/0002-session-based-recovery.md) and
  [Session archive protection](docs/adr/0004-session-archive-protection.md)
  are superseded by aws-design §6.2 and §9.3 and kept as history.
- [Infrastructure](infra/README.md) describes the OpenTofu root that builds the
  host and the controller, its state, the Deploy workflow that applies it, the
  deploy role, how to rebuild the host and how to deploy the controller.
- [Deploy from GitHub Actions](docs/adr/0007-deploy-from-github-actions.md)
  records how changes reach AWS: a Deploy run Craig starts and approves, which
  applies only the plan he read, as a role that can't step outside Foundry.
- [Backend architecture](docs/adr/0003-serverless-management-backend.md) records
  the Go Lambda controller direction; its notes mark the parts later replaced
  (recovery storage, invocation, and The Lobby's start and stop).
- [Status API](docs/status-api.md) documents the controller's HTTP interface:
  `GET /server`, which The Lobby's site serves at `/api/foundry/server` for its
  `/foundry` page, and `POST /server/start` and `POST /server/stop` for its
  Members with Foundry access ([ADR 0006](docs/adr/0006-the-lobby-starts-and-stops-the-host.md)).

## Development

Use a branch and pull request for changes to `main`. The security workflow scans
Git history and current files for secrets and checks GitHub Actions workflows.
Dependabot checks Go modules and GitHub Actions dependencies weekly.

Run `python3 -m unittest discover -s tests -v` for the offline host contract
suite. The Host contract workflow also parses the systemd templates without
starting services. The host tooling uses only the Python standard library.

The example manifest intentionally has unresolved host-specific pins. Read the
host contract before rendering a private installation tree. Rendering never
installs files or contacts the host.

The controller is a Go HTTP server that reports the host's status to The
Lobby, and starts and stops the host for it. On Lambda it serves the site
through an `AWS_IAM` Function URL, one function per Lobby site environment
([ADR 0005](docs/adr/0005-the-lobby-calls-the-controller-through-a-function-url.md),
[ADR 0006](docs/adr/0006-the-lobby-starts-and-stops-the-host.md),
[infra/controller.tf](infra/controller.tf)); `task run` serves it on your
computer with your AWS credentials. It reads EC2, the Systems Manager agent,
Foundry's join page and the stop recorded on the host. Local recovery-set
capture/validation/materialization is available for offline testing. Backups
are the Data Lifecycle Manager snapshots ([infra](infra/README.md)); End
session takes no backup of its own ([#7](https://github.com/CraigDevJohnson/foundry/issues/7)).

Use Go 1.27.2 or newer and [Task](https://taskfile.dev) 3.x, and run `task ci`
for formatting, vet, race-enabled tests, a Linux ARM64 Lambda build, and
vulnerability checks. `task build` produces `dist/bootstrap.zip`, the same
zip for the same source. See the [status reference](docs/status-api.md) for
its configuration and for running it locally.

The OpenTofu root in `infra/` uses OpenTofu 1.12.x. CI runs `tofu fmt -check`
and `tofu validate` on it without AWS credentials. Read
[infra/README.md](infra/README.md) before running a plan.

## Security

See [SECURITY.md](SECURITY.md) for private vulnerability reporting.
