# Agent guidance

## Project

The management plane for Craig's hosted Foundry Virtual Tabletop: a Go
controller on AWS Lambda that The Lobby calls to show the host's status and to
start and stop it (`cmd/`, `internal/`), the OpenTofu that builds the host,
the controller and the snapshots (`infra/`) and the deploy role
(`infra/bootstrap/`), offline host tooling in the Python standard library
(`host/`, `tests/`), and the docs (`docs/`). Read `README.md`, `CONTEXT.md`
for the terms, and `CONTRIBUTING.md` for how changes land.

## Commands

- Go: `task ci` (formatting, vet, race tests, the Lambda build, govulncheck); `task fmt` formats.
- OpenTofu: `tofu fmt -check -recursive infra`, then `tofu -chdir=infra init -backend=false -input=false -lockfile=readonly`, `validate` and `test`; the same three in `infra/bootstrap`.
- Host tooling: `python3 -m unittest discover -s tests -v`.
- The controller on your computer: `task run` (needs AWS read credentials).

## Rules

- AWS: CraigDevJohnson/aws-setup's `aws-design.md` and `AGENTS.md` govern. Agents may run `tofu init`, `validate`, `fmt`, `test` and `plan` without asking. Anything that can change AWS (`tofu apply`, `destroy`, `import`, `state` or `force-unlock`, or any other AWS call with an admin profile or root) needs Craig's approval of that specific change first. `.claude/settings.json` asks before these.
- `infra` reaches AWS only through the Deploy workflow, which Craig starts and approves (`docs/adr/0007-deploy-from-github-actions.md`).
- Commit messages and pull request titles: one plain imperative sentence (see `CONTRIBUTING.md`).
- Never commit or print credentials, state or plan files, worlds or player data.

## Agent skills

### Issue tracker

Track issues and specs in GitHub Issues for `CraigDevJohnson/foundry` using `gh`. See `docs/agents/issue-tracker.md`.

### Triage labels

Use the default labels: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, and `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Use a single-context layout with root `CONTEXT.md` and `docs/adr/`, created when needed. See `docs/agents/domain.md`.
