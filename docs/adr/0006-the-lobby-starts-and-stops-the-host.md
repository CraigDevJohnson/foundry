---
status: accepted
date: 2026-10-14
---

# The Lobby starts and stops the host

Craig chose start and stop for The Lobby (2026-10-14). The controller gets
two routes, `POST /server/start` and `POST /server/stop`
([status API](../status-api.md)), and The Lobby's site offers them to Members
with its `foundry-admin` grant; its player page at `/foundry` stays status
only.

A start is one EC2 `StartInstances` on the host. A stop sends the host a new
Systems Manager command document, `foundry-stop`. It runs the same clean
shutdown as `task host:stop` (`templates/foundry-shutdown.sh`: stop Foundry,
check no process is left writing its data, sync `/foundry`), then
`templates/foundry-power-off.sh`, which schedules `shutdown --poweroff +1`.
The host's instance-initiated shutdown behavior is `stop`, now stated in
`host.tf`, so EC2 stops the instance and keeps its volumes and Elastic IP. If
any part of the shutdown fails, the script exits before the power-off and the
host keeps running, as with `task host:stop`.

The controller never waits for either. It answers `202` at once, and the
status follows the change from there: a stop sent since EC2 last started the
host and still running, or succeeded less than 15 minutes ago, makes a running
host `stopping` (`checks.stop`, read with `ssm:ListCommands`). The controller
can't see who is playing, so a stop needs the Member's confirmation,
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

## Consequences

- The controller role gains `ec2:StartInstances` on the host,
  `ssm:SendCommand` on the host and the `foundry-stop` document only, and
  `ssm:ListCommands`, which has no resource-level permissions. It still can't
  call `ec2:StopInstances` or send any other document.
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
- Both site environments' controllers act on the one host, so a Foundry Admin
  on the dev site can stop the host that production shows.
- Start session and End session (CONTEXT.md), with a backup before the stop,
  are still separate work. This stop takes no backup, like `task host:stop`.
