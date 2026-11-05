# Contributing

foundry is Craig's personal project: the tools that run his hosted Foundry
Virtual Tabletop game. Read the [README](README.md) and the
[glossary](CONTEXT.md) first.

## How changes land

1. Branch from `main` (fork first if you can't push here).
2. Make the change and run the checks below.
3. Open a pull request against `main`. The pull request template asks for the
   checks you ran and whether a Deploy run is needed afterwards.
4. Codex reviews every pull request. Resolve every review thread.
5. `main` takes changes only through pull requests, with linear history: use a
   squash merge. The Secret scan and Workflow security checks must pass, and
   the branch must be up to date with `main`.

## Checks

Run these before you push. CI runs the same ones.

| What changed | Command |
|---|---|
| Go code (`cmd/`, `internal/`) | `task ci` (formatting, vet, race tests, the Lambda build and govulncheck) |
| OpenTofu (`infra/`) | `tofu fmt -check -recursive infra`, then `init -backend=false`, `validate` and `test` in `infra` and `infra/bootstrap` |
| Host tooling (`host/`, `tests/`) | `python3 -m unittest discover -s tests -v` |
| Workflows (`.github/`) | The Security workflow runs Gitleaks, actionlint and zizmor |

Use Go 1.27.3 or newer, [Task](https://taskfile.dev) 3.x and OpenTofu 1.12.x.

## Changes that reach AWS

Merging a pull request changes nothing in AWS. The `infra` root reaches AWS
only through the Deploy workflow, which Craig starts and approves, and which
applies only the plan he read ([ADR 0007](docs/adr/0007-deploy-from-github-actions.md)).
Craig applies `infra/bootstrap` by hand. Read [infra/README.md](infra/README.md)
before you run a plan.

## Commit messages and pull request titles

Write one plain sentence in the imperative that says what changes, for
example `Serve Foundry at https://foundry.craigdevjohnson.com through nginx on
the host`. Dependabot keeps its own `build(deps):` titles.

## Issues

Use the issue forms for bugs, feature requests and questions. New issues get
`needs-triage`. The triage labels are described in
[docs/agents/triage-labels.md](docs/agents/triage-labels.md), and `P0` to `P3`
set priority (`P0` is an emergency, such as lost world data).

## Keep out of Git

Credentials, `.env` files, OpenTofu state and plan files, Foundry worlds and
player data. Report a vulnerability through the [security policy](SECURITY.md),
not in an issue.

## Conduct

Follow the [code of conduct](CODE_OF_CONDUCT.md).
