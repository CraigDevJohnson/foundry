# Foundry

The management plane for Craig's hosted Foundry Virtual Tabletop (Foundry VTT)
application and the AWS EC2 instance that runs it.

This repository will own the tools for operating that installation. The first
release serves Craig as its sole operator and supports a game with at most five
concurrent users. The portfolio provides the management interface; this
repository owns the Foundry backend, operations, and reproducible host and
service setup. The operating policy and backend architecture were confirmed
through the design interview on 2026-09-08.

## Agreed first-release scope

- View the status of the host and Foundry VTT application.
- Provide explicit Start session and End session actions: start the host for
  play or solo GM preparation, then back up the installation before stopping it.
- Restart the Foundry VTT service separately from the host.
- Back up the installation and verify that it can be recovered.
- Define how to recreate the host and service configuration, adopting the
  existing host carefully after its configuration and backups are verified.

## Operating priorities

- Preserve existing worlds and their data.
- Keep the game available during play; continuous availability outside play is
  not required.
- Schedule disruptive maintenance outside play.
- Take a manual snapshot of both volumes before upgrades. Upgrade automation
  itself has not been selected for the first release.
- Protect every completed session. Loss of progress from a session in progress
  is acceptable if storage fails; recover with a tested, guided restore.
- Use manual session controls initially; automatic idle shutdown and a fixed
  start/stop schedule are outside the agreed first-release scope.
- Require the operator to confirm that nobody will be interrupted before
  End session or a service restart. Automatic player detection is not required.
- If End session cannot confirm a successful backup, keep the host running,
  report the failure, and require a successful retry before normal shutdown.
- Back up both EBS volumes with a Data Lifecycle Manager policy in us-west-2:
  daily snapshots at 10:00 UTC keeping 7, and weekly snapshots on Sundays at
  11:00 UTC keeping 4 ([infra](infra/README.md)). Keep each manual pre-upgrade
  snapshot until the upgrade is verified. Recovery is best effort, about
  24 hours; there is no recovery Region, cross-Region copy, vault lock or legal
  hold ([aws-design §6.2 and §9.3](https://github.com/CraigDevJohnson/aws-setup/blob/main/aws-design.md#62-foundry-game-server-repo-foundry)).
- Test a restore once after the snapshot policy is set up. There is no
  restore-drill calendar.
- Keep costs appropriate for a small personal game server. No fixed monthly cap
  has been chosen; proposals must explain any increase to the current operating
  cost.

The backend's implementation and integration contracts remain engineering work.
Live adoption must address the service supervision and renewal findings in the
hosting baseline and demonstrate startup, backup, and recovery before the
management plane is relied upon.

## Project documentation

- [Domain glossary](CONTEXT.md) defines the project's terms.
- [Operations](docs/operations.md) covers running the workloads host: DNS,
  certificate renewal, snapshots, restores, the license and the move record.
- [Hosting baseline](docs/hosting-baseline.md) is historical: the old
  management-account host and related management tools as found in discovery.
- [Host lifecycle contract](docs/host-contract.md) defines the pinned inputs,
  filesystem paths, fixed commands, and offline checks for issue #5.
- [Local recovery sets](docs/local-recovery-sets.md) describes the fixture-tested capture,
  integrity verification and isolated materialization tools; cloud protection and
  live restore remain pending.
- [Host adoption and rollback](docs/host-adoption.md) is historical: the
  adoption procedure for the old host, replaced by the move to workloads.
- [First-release specification](https://github.com/CraigDevJohnson/foundry/issues/2)
  contains the consolidated scope, acceptance checks, and adoption prerequisites.
- [Portfolio and Foundry responsibilities](docs/adr/0001-portfolio-and-foundry-responsibilities.md)
  records the management boundary.
- [Session-based recovery](docs/adr/0002-session-based-recovery.md) and
  [Session archive protection](docs/adr/0004-session-archive-protection.md)
  are superseded by aws-design §6.2 and §9.3 and kept as history.
- [Infrastructure](infra/README.md) describes the OpenTofu root that builds the
  host, its state and how to rebuild the host.
- [Backend architecture](docs/adr/0003-serverless-management-backend.md) records
  the accepted Go Lambda and Systems Manager implementation direction.
- [Status API](docs/status-api.md) documents the first read-only backend slice.

## Development

Use a branch and pull request for changes to `main`. The security workflow scans
Git history and current files for secrets and checks GitHub Actions workflows.
Dependabot checks GitHub Actions dependencies weekly.

Run `python3 -m unittest discover -s tests -v` for the offline host contract
suite. The Host contract workflow also parses the systemd templates without
starting services. The host tooling uses only the Python standard library.

The example manifest intentionally has unresolved host-specific pins. Read the
host contract before rendering a private installation tree. Rendering never
installs files or contacts the host.

The first implementation slice provides a Go Lambda status controller and a
local read-only runner. It reports host, agent, and HTTPS observations. Local
recovery-set capture/validation/materialization is now available for offline testing;
cloud backups, session operations, live recovery, infrastructure and portfolio
integration
remain tracked in the [first-release backlog](https://github.com/CraigDevJohnson/foundry/issues/2).

Use Go 1.27.1 or newer and [Task](https://taskfile.dev) 3.x, and run `task ci`
for formatting, vet, race-enabled tests, a Linux ARM64 Lambda build, and
vulnerability checks. `task build` produces `dist/bootstrap.zip`. See the [status reference](docs/status-api.md) for required
configuration and local invocation.

The OpenTofu root in `infra/` uses OpenTofu 1.12.x. CI runs `tofu fmt -check`
and `tofu validate` on it without AWS credentials. Read
[infra/README.md](infra/README.md) before running a plan.

## Security

See [SECURITY.md](SECURITY.md) for private vulnerability reporting.
