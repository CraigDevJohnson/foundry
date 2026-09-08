# Issue tracker: GitHub

Issues and specs for this repo live in [CraigDevJohnson/foundry GitHub Issues](https://github.com/CraigDevJohnson/foundry/issues). Use the `gh` CLI for all operations.

## Conventions

- **Create an issue**: `gh issue create --title "..." --body-file /path/to/body.md`. Write multi-line bodies to a file with a quoted heredoc so newlines and literal text are preserved.
- **Read an issue**: `gh issue view <number> --json number,title,body,labels,comments` or `gh issue view <number> --comments`.
- **List issues**: `gh issue list --state open --limit 100 --json number,title,body,labels,comments`, with appropriate `--label` and `--state` filters. For a complete inventory, use `gh api --paginate 'repos/CraigDevJohnson/foundry/issues?state=all'` and filter out entries containing `pull_request`.
- **Comment on an issue**: `gh issue comment <number> --body-file /path/to/comment.md`.
- **Apply / remove labels**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`.
- **Close**: `gh issue close <number> --comment "..."`.

Infer the repo from `git remote -v`; `gh` does this automatically when run inside this clone. Outside the clone, pass `--repo CraigDevJohnson/foundry` to issue commands.

## Pull requests as a triage surface

**PRs as a request surface: no.** _(Set to `yes` if this repo treats external PRs as feature requests; `/triage` reads this flag.)_

GitHub shares one number space across issues and PRs, so a bare `#42` may be either: resolve with `gh pr view 42` and fall back to `gh issue view 42`.

## When a skill says "publish to the issue tracker"

Create a GitHub issue.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --comments` and fetch its labels.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a single issue with **child** issues as tickets.

- **Map**: a single issue labelled `wayfinder:map`, holding the Notes / Decisions-so-far / Fog body. Use `gh issue create --label wayfinder:map` with a title and body file.
- **Child ticket**: an issue linked to the map as a GitHub sub-issue using `gh api`. Where sub-issues are unavailable, add the child to a task list in the map body and put `Part of #<map>` at the top of the child body. Labels: `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). Once claimed, assign the ticket to the driving developer.
- **Blocking**: use GitHub's native issue dependencies. Add an edge with `gh api --method POST repos/CraigDevJohnson/foundry/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>`, where the blocker database ID comes from `gh api repos/CraigDevJohnson/foundry/issues/<number> --jq .id`; it is not the issue number or `node_id`. `issue_dependencies_summary.blocked_by` counts open blockers. Where dependencies are unavailable, put `Blocked by: #<number>, #<number>` at the top of the child body. A ticket is unblocked when every blocker is closed.
- **Frontier query**: list the map's open children, scoped to its sub-issues or task list. Exclude tickets with an open blocker or an assignee; first in map order wins.
- **Claim**: `gh issue edit <number> --add-assignee @me`, the session's first write.
- **Resolve**: comment with the answer, close the ticket, then append a context pointer (brief summary and link) to the map's Decisions-so-far.

Create any wayfinding labels only when adopting that workflow; they are separate from the five default triage labels.
