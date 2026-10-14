---
status: accepted
date: 2026-10-14
---

# The Lobby starts and stops the host

Craig chose start and stop for The Lobby (2026-10-14). The controller gets
two routes, `POST /server/start` and `POST /server/stop`
([status API](../status-api.md)), and The Lobby's site offers them to every
Member with Foundry access, its `foundry` or `foundry-admin` grant (Craig's
choice, 2026-10-14). Its player page at `/foundry` stays status only until a
later page adds the buttons.

A start is one EC2 `StartInstances` on the host. A stop sends the host a new
Systems Manager command document, `foundry-stop`
(`templates/foundry-stop.sh.tftpl`). It writes the same clean shutdown as
`task host:stop` (`templates/foundry-shutdown.sh`: stop Foundry, check no
process is left writing its data, sync `/foundry`) to a file of its own and
runs it, and only if that exits 0 runs `templates/foundry-power-off.sh`, which
schedules `shutdown --poweroff +1`. The file keeps the stop script's own
command line, which holds the shutdown's text, out of the shutdown's search
for processes writing Foundry data. The host's instance-initiated shutdown
behavior is `stop`, now stated in `host.tf`, so EC2 stops the instance and
keeps its volumes and Elastic IP. If any part of the shutdown fails, the
power-off never runs and the host keeps running, as with `task host:stop`.
Each site environment has its own controller, so two stops can arrive at
once; a lock in `/run` lets only one run the shutdown, and the other waits
for it and ends as it did.

The controller never waits for either. It answers `202` at once, and the
status follows the change from there: a stop sent since EC2 last started the
host and still running, or succeeded less than 15 minutes after it was sent,
makes a running host `stopping` (`checks.stop`). The controller records each
stop it sends on the host's own tags, `foundry:stop-command` (the command's
ID) and `foundry:stop-requested` (when it was sent), and the status follows
that one command with `ssm:GetCommandInvocation`. The controller can't see who
is playing, so a stop needs the Member's confirmation,
`{"nobody_playing": true}`, which the site's page asks for.

Each start and stop is logged with who asked. The Function URL's request
context carries the caller AWS verified; when that is the environment's site
role (`FOUNDRY_SITE_ROLE_ARN`), the Member the site names in `X-Lobby-Member`
is logged with it, as ADR 0005 planned.

## Considered options

- Systems Manager Automation: a runbook that runs the shutdown and then
  `aws:changeInstanceState`. Rejected: AWS's documentation could not confirm
  whether `aws:runCommand` waits for its command or which permissions the
  automation role needs, and it adds a role, `iam:PassRole` and an execution
  history to read for what one command can do.
- The Minecraft Launcher's shape: the function invokes itself to finish the
  stop, waiting for the shutdown and then calling `StopInstances`. Rejected:
  it needs a store for the stop's progress and a 15-minute function, for a
  sequence the host can finish itself.
- The controller calling `StopInstances` after the shutdown. Rejected for the
  same reason: someone has to wait for the shutdown, and the site gives up
  after 7 seconds.
- Finding the stops with `ssm:ListCommands`. Rejected: it has no
  resource-level permissions, so the controller's role could read every
  command sent in the account with its parameters, the Minecraft Launcher's
  included. The host's tags hold the one command the status needs.

## Consequences

- The controller role gains `ec2:StartInstances` on the host,
  `ssm:SendCommand` on the host and the `foundry-stop` document only,
  `ec2:CreateTags` on the host for its two stop tags only, and
  `ssm:GetCommandInvocation`, which has no resource-level permissions but
  answers only for a command ID the caller already has. It still can't call
  `ec2:StopInstances`, send any other document or list commands. The
  OpenTofu provider ignores the two stop tags, so a plan never removes them.
- If the tags can't be written after the stop is sent, the controller still
  answers `202` and logs `sent_not_recorded` as an error. The status can't
  show that stop until the host powers off, and doesn't hold off a second
  one, which finds Foundry already down.
- A stop that fails, times out or is cancelled alerts through the
  `foundry-stop-failed` EventBridge rule to the workloads `alerts` topic. The
  host is still running then, and `task host:stop` or the Host control
  workflow are the fallback.
- The stop is sent with a 60-second delivery timeout. AWS's documentation
  differs on whether an undelivered command expires then or 5 minutes later
  (the document's execution timeout), so a stop the agent misses can run, or
  fail, up to about 6 minutes after it was asked for. The status shows the
  host `stopping` for as long as it is pending. A host whose agent is not
  connected yet, for example just after a start, answers `409
  agent_offline`.
- Both site environments' controllers act on the one host, so a Member on
  the dev site can stop the host that production shows.
- Start session and End session (CONTEXT.md), with a backup before the stop,
  are still separate work. This stop takes no backup, like `task host:stop`.
