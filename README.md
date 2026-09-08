# Foundry

The management plane for Craig's hosted Foundry Virtual Tabletop (Foundry VTT)
application and the AWS EC2 instance that runs it.

This repository will own the tools for operating that installation. The first
release serves Craig as its sole operator and supports a game with at most five
concurrent users. The portfolio provides the management interface; this
repository owns the Foundry backend, operations, and reproducible host and
service setup. The operating policy below captures the agreed design interview
answers and is awaiting a final shared-understanding review before implementation.

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
- Require a recoverable backup before upgrades. Upgrade automation itself has
  not been selected for the first release.
- Protect every completed session. Loss of progress from a session in progress
  is acceptable if storage fails; use a tested, guided restore that Craig can
  complete the same day.
- Use manual session controls initially; automatic idle shutdown and a fixed
  start/stop schedule are outside the agreed first-release scope.
- Require the operator to confirm that nobody will be interrupted before
  End session or a service restart. Automatic player detection is not required.
- If End session cannot confirm a successful backup, keep the host running,
  report the failure, and require a successful retry before normal shutdown.
- Retain the latest 10 successful session backups, plus pre-upgrade backups
  until the operator explicitly releases them.
- Check every backup's integrity. Prove recovery through an actual isolated
  restore before first use, after relevant backup/recovery or infrastructure
  changes, and quarterly; include temporary recovery resources in the cost
  estimate.
- Keep costs appropriate for a small personal game server. No fixed monthly cap
  has been chosen; proposals must explain any increase to the current operating
  cost.

The backend's implementation and integration contracts remain engineering work.
Live adoption must address the service supervision and renewal findings in the
hosting baseline and demonstrate startup, backup, and recovery before the
management plane is relied upon.

## Project documentation

- [Domain glossary](CONTEXT.md) defines the project's terms.
- [Hosting baseline](docs/hosting-baseline.md) records the existing installation
  and related management tools verified during discovery.
- [First-release specification](https://github.com/CraigDevJohnson/foundry/issues/2)
  contains the consolidated scope, acceptance checks, and adoption prerequisites.
- [Portfolio and Foundry responsibilities](docs/adr/0001-portfolio-and-foundry-responsibilities.md)
  records the management boundary.
- [Session-based recovery](docs/adr/0002-session-based-recovery.md) records the
  availability and recovery trade-off.
- [Proposed backend architecture](docs/adr/0003-serverless-management-backend.md)
  describes the recommended implementation for final design review.

## Development

Use a branch and pull request for changes to `main`. The security workflow scans
Git history and current files for secrets and checks GitHub Actions workflows.
Dependabot checks GitHub Actions dependencies weekly.

Application code and dependency manifests have not been added yet. Add suitable
code and dependency scans when the application stack is chosen.

## Security

See [SECURITY.md](SECURITY.md) for private vulnerability reporting.
